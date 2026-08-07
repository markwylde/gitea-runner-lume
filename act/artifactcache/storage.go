// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2023 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package artifactcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Storage struct {
	rootDir string
}

func NewStorage(rootDir string) (*Storage, error) {
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		return nil, err
	}
	return &Storage{
		rootDir: rootDir,
	}, nil
}

func (s *Storage) Exist(id uint64) (bool, error) {
	name := s.filename(id)
	if _, err := os.Stat(name); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Storage) Write(id uint64, offset int64, reader io.Reader) error {
	return s.writeFile(s.tempName(id, offset), reader)
}

func (s *Storage) writeFile(name string, reader io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	file, err := os.Create(name)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, reader)
	return err
}

func (s *Storage) WriteBlock(id uint64, blockID string, reader io.Reader) error {
	return s.writeFile(s.blockName(id, blockID), reader)
}

// OrderBlocks renames the staged blocks into the order the block list gives. A block the list
// does not name keeps its staged name, which is how Commit leaves it out, as Azure drops it. One
// rename pass is safe because a staged name always carries blockFilePrefix and a target name
// never does, so no rename can collide with a block not yet moved.
func (s *Storage) OrderBlocks(id uint64, blockIDs []string) error {
	for i, blockID := range blockIDs {
		if err := os.Rename(s.blockName(id, blockID), s.tempName(id, int64(i))); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("block %q of cache %d was never uploaded: %w", blockID, id, err)
			}
			return err
		}
	}
	return nil
}

func (s *Storage) Commit(id uint64, size int64) (int64, error) {
	defer func() {
		_ = os.RemoveAll(s.tempDir(id))
	}()

	name := s.filename(id)
	tempNames, err := s.tempNames(id)
	if err != nil {
		return 0, err
	}

	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return 0, err
	}
	written, err := assemble(name, tempNames)
	if err != nil {
		return 0, err
	}
	// If size is less than 0, it means the size is unknown.
	// We can't check the size of the file, just skip the check.
	// It happens when the request comes from old versions of actions, like `actions/cache@v2`.
	if size >= 0 && written != size {
		_ = os.Remove(name)
		return 0, fmt.Errorf("broken file: %v != %v", written, size)
	}
	return written, nil
}

// assemble concatenates the uploaded parts into name. A single part, which is what the v2 API
// produces below the client's block threshold, is already the whole archive and is moved.
func assemble(name string, tempNames []string) (int64, error) {
	if len(tempNames) == 1 {
		info, err := os.Stat(tempNames[0])
		if err != nil {
			return 0, err
		}
		return info.Size(), os.Rename(tempNames[0], name)
	}

	file, err := os.Create(name)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var written int64
	for _, v := range tempNames {
		f, err := os.Open(v)
		if err != nil {
			return 0, err
		}
		n, err := io.Copy(file, f)
		_ = f.Close()
		if err != nil {
			return 0, err
		}
		written += n
	}
	return written, nil
}

func (s *Storage) Serve(w http.ResponseWriter, r *http.Request, id uint64) {
	name := s.filename(id)
	http.ServeFile(w, r, name)
}

func (s *Storage) Remove(id uint64) {
	_ = os.Remove(s.filename(id))
	_ = os.RemoveAll(s.tempDir(id))
}

func (s *Storage) filename(id uint64) string {
	return filepath.Join(s.rootDir, fmt.Sprintf("%02x", id%0xff), strconv.FormatUint(id, 10))
}

func (s *Storage) tempDir(id uint64) string {
	return filepath.Join(s.rootDir, "tmp", strconv.FormatUint(id, 10))
}

func (s *Storage) tempName(id uint64, offset int64) string {
	return filepath.Join(s.tempDir(id), fmt.Sprintf("%016x", offset))
}

// blockFilePrefix marks a staged, not yet ordered block, so that tempNames can keep it out of
// Commit's name-ordered concatenation.
const blockFilePrefix = "block-"

func (s *Storage) blockName(id uint64, blockID string) string {
	// The block id is client-chosen (base64), so it is hashed rather than trusted as a
	// path element.
	sum := sha256.Sum256([]byte(blockID))
	return filepath.Join(s.tempDir(id), blockFilePrefix+hex.EncodeToString(sum[:]))
}

func (s *Storage) tempNames(id uint64) ([]string, error) {
	dir := s.tempDir(id)
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, v := range files {
		if !v.IsDir() && !strings.HasPrefix(v.Name(), blockFilePrefix) {
			names = append(names, filepath.Join(dir, v.Name()))
		}
	}
	return names, nil
}
