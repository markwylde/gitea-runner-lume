// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestproto

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const (
	Version         = 1
	MaxMessageBytes = 8 * 1024 * 1024
	MaxSessionBytes = 128 * 1024 * 1024
)

var (
	magic            = [4]byte{'G', 'R', 'L', '1'}
	hex32Pattern     = regexp.MustCompile(`^[a-f0-9]{32}$`)
	hex64Pattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	messageTypeValid = regexp.MustCompile(`^[a-z][a-z_]{1,31}$`)
)

type Message struct {
	Version int             `json:"version"`
	Type    string          `json:"type"`
	Seq     uint64          `json:"seq"`
	Body    json.RawMessage `json:"body"`
}

type Hello struct {
	InstallationID string `json:"installation_id"`
	LeaseID        string `json:"lease_id"`
	WorkerID       string `json:"worker_id"`
	TaskID         int64  `json:"task_id"`
	Nonce          string `json:"nonce"`
	Revision       string `json:"revision"`
	OS             string `json:"os,omitempty"`
	Architecture   string `json:"architecture,omitempty"`
	OSVersion      string `json:"os_version,omitempty"`
	AgentSHA256    string `json:"agent_sha256,omitempty"`
	UID            int    `json:"uid,omitempty"`
	Signature      string `json:"signature"`
}

type Task struct {
	Payload []byte `json:"payload"`
}

type Event struct {
	Kind    string `json:"kind"`
	Payload []byte `json:"payload,omitempty"`
}

type Result struct {
	Conclusion string `json:"conclusion"`
	ErrorClass string `json:"error_class,omitempty"`
}

type ExecRequest struct {
	Command []string          `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
	User    string            `json:"user,omitempty"`
	Workdir string            `json:"workdir"`
}

type ExecResult struct {
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

type File struct {
	Name string `json:"name"`
	Mode int64  `json:"mode"`
	Body []byte `json:"body"`
}

type CopyRequest struct {
	Destination string `json:"destination"`
	Files       []File `json:"files"`
}

type ArchiveRequest struct {
	Path string `json:"path"`
}

type CopyTarBegin struct {
	Destination string `json:"destination"`
}

type Data struct {
	Bytes []byte `json:"bytes"`
}

type Error struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

func NewNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func SignHello(hello Hello, privateKey ed25519.PrivateKey) (Hello, error) {
	if err := hello.validateUnsigned(); err != nil {
		return Hello{}, err
	}
	hello.Signature = hex.EncodeToString(ed25519.Sign(privateKey, hello.transcript()))
	return hello, nil
}

func VerifyHello(hello Hello, publicKey ed25519.PublicKey, expected Hello) error {
	if err := hello.validateUnsigned(); err != nil {
		return err
	}
	if hello.InstallationID != expected.InstallationID || hello.LeaseID != expected.LeaseID ||
		hello.WorkerID != expected.WorkerID || hello.TaskID != expected.TaskID ||
		hello.Nonce != expected.Nonce || hello.Revision != expected.Revision {
		return errors.New("guest hello does not match the expected session")
	}
	if expected.OS != "" && (hello.OS != expected.OS || hello.Architecture != expected.Architecture ||
		hello.OSVersion != expected.OSVersion || hello.AgentSHA256 != expected.AgentSHA256 || hello.UID != expected.UID) {
		return errors.New("guest hello does not match the expected attestation")
	}
	signature, err := hex.DecodeString(hello.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("guest hello signature is invalid")
	}
	if !ed25519.Verify(publicKey, hello.transcript(), signature) {
		return errors.New("guest hello signature verification failed")
	}
	return nil
}

func (h Hello) validateUnsigned() error {
	if !hex32Pattern.MatchString(h.InstallationID) || !hex32Pattern.MatchString(h.LeaseID) ||
		!hex32Pattern.MatchString(h.WorkerID) || !hex64Pattern.MatchString(h.Nonce) {
		return errors.New("guest hello identity is invalid")
	}
	if h.TaskID <= 0 || h.Revision == "" || len(h.Revision) > 128 {
		return errors.New("guest hello task or revision is invalid")
	}
	attested := h.OS != "" || h.Architecture != "" || h.OSVersion != "" || h.AgentSHA256 != "" || h.UID != 0
	if attested && (h.OS == "" || h.Architecture == "" || h.OSVersion == "" || !hex64Pattern.MatchString(h.AgentSHA256) || h.UID <= 0 ||
		len(h.OS) > 32 || len(h.Architecture) > 32 || len(h.OSVersion) > 128) {
		return errors.New("guest hello attestation is invalid")
	}
	return nil
}

func (h Hello) transcript() []byte {
	return []byte(fmt.Sprintf("grl-guest-v%d\n%s\n%s\n%s\n%d\n%s\n%s\n%s\n%s\n%s\n%s\n%d", Version,
		h.InstallationID, h.LeaseID, h.WorkerID, h.TaskID, h.Nonce, h.Revision,
		h.OS, h.Architecture, h.OSVersion, h.AgentSHA256, h.UID))
}

type Writer struct {
	w       io.Writer
	nextSeq uint64
	total   uint64
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w, nextSeq: 1}
}

func (w *Writer) Write(messageType string, body any) error {
	if !messageTypeValid.MatchString(messageType) {
		return errors.New("guest message type is invalid")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	message, err := json.Marshal(Message{Version: Version, Type: messageType, Seq: w.nextSeq, Body: payload})
	if err != nil {
		return err
	}
	if len(message) > MaxMessageBytes {
		return errors.New("guest message exceeds size limit")
	}
	frameBytes := uint64(len(magic) + 4 + len(message))
	if w.total+frameBytes > MaxSessionBytes {
		return errors.New("guest session exceeds size limit")
	}
	var header [8]byte
	copy(header[:4], magic[:])
	binary.BigEndian.PutUint32(header[4:], uint32(len(message)))
	if _, err := w.w.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.w.Write(message); err != nil {
		return err
	}
	w.nextSeq++
	w.total += frameBytes
	return nil
}

type Reader struct {
	r       *bufio.Reader
	nextSeq uint64
	total   uint64
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r), nextSeq: 1}
}

func (r *Reader) Read(expectedType string, destination any) error {
	message, err := r.ReadMessage()
	if err != nil {
		return err
	}
	if message.Type != expectedType {
		return errors.New("guest message type is invalid")
	}
	return DecodeBody(message, destination)
}

func (r *Reader) ReadMessage() (Message, error) {
	var header [8]byte
	if _, err := io.ReadFull(r.r, header[:]); err != nil {
		return Message{}, err
	}
	if !bytes.Equal(header[:4], magic[:]) {
		return Message{}, errors.New("guest frame magic is invalid")
	}
	length := binary.BigEndian.Uint32(header[4:])
	if length == 0 || length > MaxMessageBytes {
		return Message{}, errors.New("guest frame length is invalid")
	}
	frameBytes := uint64(len(header)) + uint64(length)
	if r.total+frameBytes > MaxSessionBytes {
		return Message{}, errors.New("guest session exceeds size limit")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r.r, payload); err != nil {
		return Message{}, err
	}
	var message Message
	if err := decodeStrict(payload, &message); err != nil {
		return Message{}, fmt.Errorf("decode guest message: %w", err)
	}
	if message.Version != Version || message.Seq != r.nextSeq || !messageTypeValid.MatchString(message.Type) {
		return Message{}, errors.New("guest message version, sequence, or type is invalid")
	}
	r.nextSeq++
	r.total += frameBytes
	return message, nil
}

func DecodeBody(message Message, destination any) error {
	if destination == nil {
		return nil
	}
	if err := decodeStrict(message.Body, destination); err != nil {
		return fmt.Errorf("decode guest message body: %w", err)
	}
	return nil
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
