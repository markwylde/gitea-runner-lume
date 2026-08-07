// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func testHello() Hello {
	return Hello{
		InstallationID: "0123456789abcdef0123456789abcdef",
		LeaseID:        "abcdef0123456789abcdef0123456789",
		WorkerID:       "11111111111111111111111111111111",
		TaskID:         42,
		Nonce:          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Revision:       "24c13a1fd0cac7981c0bb6af4aeeedb03f44ff46",
	}
}

func TestHelloSignatureBindsTheCompleteSession(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	expected := testHello()
	signed, err := SignHello(expected, privateKey)
	require.NoError(t, err)
	require.NoError(t, VerifyHello(signed, publicKey, expected))

	tampered := signed
	tampered.TaskID++
	require.Error(t, VerifyHello(tampered, publicKey, expected))
}

func TestHelloRejectsMITMAndEveryBoundIdentity(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, attackerKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	expected := testHello()
	signed, err := SignHello(expected, privateKey)
	require.NoError(t, err)

	for name, mutate := range map[string]func(*Hello){
		"installation": func(h *Hello) { h.InstallationID = "22222222222222222222222222222222" },
		"lease":        func(h *Hello) { h.LeaseID = "22222222222222222222222222222222" },
		"worker":       func(h *Hello) { h.WorkerID = "22222222222222222222222222222222" },
		"task":         func(h *Hello) { h.TaskID++ },
		"nonce":        func(h *Hello) { h.Nonce = "22" + h.Nonce[2:] },
		"revision":     func(h *Hello) { h.Revision = "other-revision" },
	} {
		t.Run(name, func(t *testing.T) {
			tampered := signed
			mutate(&tampered)
			require.Error(t, VerifyHello(tampered, publicKey, expected))
		})
	}
	attackerSigned, err := SignHello(expected, attackerKey)
	require.NoError(t, err)
	require.Error(t, VerifyHello(attackerSigned, publicKey, expected))
}

func TestHelloBindsGuestPlatformAndBinaryAttestation(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	expected := testHello()
	expected.OS = "darwin"
	expected.Architecture = "arm64"
	expected.OSVersion = "15.6"
	expected.AgentSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	expected.UID = 501
	signed, err := SignHello(expected, privateKey)
	require.NoError(t, err)
	require.NoError(t, VerifyHello(signed, publicKey, expected))

	for name, mutate := range map[string]func(*Hello){
		"os":           func(h *Hello) { h.OS = "linux" },
		"architecture": func(h *Hello) { h.Architecture = "amd64" },
		"version":      func(h *Hello) { h.OSVersion = "15.5" },
		"binary":       func(h *Hello) { h.AgentSHA256 = "b" + h.AgentSHA256[1:] },
		"uid":          func(h *Hello) { h.UID++ },
	} {
		t.Run(name, func(t *testing.T) {
			changed := expected
			mutate(&changed)
			require.Error(t, VerifyHello(signed, publicKey, changed))
		})
	}
}

func TestFramingEnforcesTypeSequenceAndStrictBodies(t *testing.T) {
	var stream bytes.Buffer
	writer := NewWriter(&stream)
	require.NoError(t, writer.Write("task", Task{Payload: []byte("opaque task")}))
	require.NoError(t, writer.Write("cancel", struct{}{}))

	reader := NewReader(&stream)
	var task Task
	require.NoError(t, reader.Read("task", &task))
	require.Equal(t, []byte("opaque task"), task.Payload)
	require.NoError(t, reader.Read("cancel", &struct{}{}))
}

func TestFramingRejectsOversizeReplayAndUnknownFields(t *testing.T) {
	var oversized bytes.Buffer
	oversized.Write(magic[:])
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], MaxMessageBytes+1)
	oversized.Write(length[:])
	require.Error(t, NewReader(&oversized).Read("task", &Task{}))

	var stream bytes.Buffer
	writer := NewWriter(&stream)
	require.NoError(t, writer.Write("task", Task{Payload: []byte("one")}))
	reader := NewReader(&stream)
	require.Error(t, reader.Read("event", &Event{}))

	var unknown bytes.Buffer
	payload := []byte(`{"version":1,"type":"task","seq":1,"body":{"payload":"eA==","extra":true}}`)
	unknown.Write(magic[:])
	binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
	unknown.Write(length[:])
	unknown.Write(payload)
	require.Error(t, NewReader(&unknown).Read("task", &Task{}))
}

func TestFramingRejectsTruncationBadMagicVersionSequenceAndTrailingJSON(t *testing.T) {
	validBody := `{"version":1,"type":"task","seq":1,"body":{"payload":"eA=="}}`
	frame := func(payload string) []byte {
		var stream bytes.Buffer
		stream.Write(magic[:])
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(payload)))
		stream.Write(length[:])
		stream.WriteString(payload)
		return stream.Bytes()
	}

	for name, input := range map[string][]byte{
		"header":   magic[:2],
		"payload":  frame(validBody)[:12],
		"magic":    append([]byte("NOPE"), frame(validBody)[4:]...),
		"version":  frame(`{"version":2,"type":"task","seq":1,"body":{}}`),
		"sequence": frame(`{"version":1,"type":"task","seq":2,"body":{}}`),
		"trailing": frame(validBody + `{}`),
	} {
		t.Run(name, func(t *testing.T) {
			var task Task
			require.Error(t, NewReader(bytes.NewReader(input)).Read("task", &task))
		})
	}
}

func FuzzReaderNeverAcceptsInvalidEnvelope(f *testing.F) {
	var valid bytes.Buffer
	require.NoError(f, NewWriter(&valid).Write("task", Task{Payload: []byte("seed")}))
	f.Add(valid.Bytes())
	f.Add([]byte("GRL1"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		message, err := NewReader(bytes.NewReader(data)).ReadMessage()
		if err != nil {
			return
		}
		require.Equal(t, Version, message.Version)
		require.Equal(t, uint64(1), message.Seq)
		require.True(t, messageTypeValid.MatchString(message.Type))
		_, err = io.Copy(io.Discard, bytes.NewReader(message.Body))
		require.NoError(t, err)
	})
}
