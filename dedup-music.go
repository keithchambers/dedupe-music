package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGenerateKey verifies that generateKey produces the expected composite key.
func TestGenerateKey(t *testing.T) {
	key := generateKey("song.mp3", "abc123")
	expected := "song.mp3|abc123"
	if key != expected {
		t.Errorf("generateKey() = %q, want %q", key, expected)
	}
}

// TestFileHash creates a temporary file with known content and verifies the computed MD5 hash.
func TestFileHash(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "testfile")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	content := []byte("hello world")
	if _, err := tmpFile.Write(content); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	hash, err := fileHash(tmpFile.Name())
	if err != nil {
		t.Fatalf("fileHash() error: %v", err)
	}
	// Expected MD5 of "hello world"
	expected := "5eb63bbbe01eeed093cb22bb8f5acdc3"
	if hash != expected {
		t.Errorf("fileHash() = %q, want %q", hash, expected)
	}
}

// TestWriteJSONToFile writes a JSON file and then decodes it to verify the output.
func TestWriteJSONToFile(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "testjson*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	filename := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(filename)

	data := []*FileInfo{
		{
			Name: "song.mp3",
			Path: "/path/to/song.mp3",
			Hash: "abc123",
			Size: 12345,
			Children: []*FileInfo{
				{Name: "song (1).mp3", Path: "/path/to/song (1).mp3", Hash: "abc123", Size: 12345},
			},
		},
	}
	if err := writeJSONToFile(filename, data); err != nil {
		t.Fatalf("writeJSONToFile() error: %v", err)
	}

	file, err := os.Open(filename)
	if err != nil {
		t.Fatalf("Failed to open written JSON file: %v", err)
	}
	defer file.Close()

	var decoded []*FileInfo
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if len(decoded) != 1 || decoded[0].Name != "song.mp3" {
		t.Errorf("Decoded JSON does not match expected output: %+v", decoded)
	}
}

// TestCopyFile creates a temporary source file and target directory, then verifies the file is copied correctly.
func TestCopyFile(t *testing.T) {
	// Create temporary source file.
	srcFile, err := os.CreateTemp("", "srcfile*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp source file: %v", err)
	}
	srcFileName := srcFile.Name()
	content := "copy test content"
	if _, err := srcFile.WriteString(content); err != nil {
		t.Fatalf("Failed to write to source file: %v", err)
	}
	srcFile.Close()
	defer os.Remove(srcFileName)

	// Create temporary target directory.
	targetDir, err := os.MkdirTemp("", "targetDir")
	if err != nil {
		t.Fatalf("Failed to create temp target directory: %v", err)
	}
	defer os.RemoveAll(targetDir)

	// Create a dummy FileInfo for the source file.
	fileInfo := &FileInfo{
		Name: filepath.Base(srcFileName),
		Path: srcFileName,
	}

	// Call copyFile.
	if err := copyFile(srcFileName, targetDir, fileInfo); err != nil {
		t.Fatalf("copyFile() error: %v", err)
	}

	// Verify that the file exists in the target directory.
	destPath := filepath.Join(targetDir, filepath.Base(srcFileName))
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("Failed to read destination file: %v", err)
	}
	if string(data) != content {
		t.Errorf("Copied file content = %q, want %q", string(data), content)
	}
}

// TestGetFileTimes verifies that getFileTimes returns a modification time close to os.Stat()'s mod time.
func TestGetFileTimes(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "testfile")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFileName := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpFileName)

	accessTime, modTime, err := getFileTimes(tmpFileName)
	if err != nil {
		t.Fatalf("getFileTimes() error: %v", err)
	}
	stat, err := os.Stat(tmpFileName)
	if err != nil {
		t.Fatalf("os.Stat() error: %v", err)
	}
	expectedModTime := stat.ModTime()
	// Allow a small delta between times.
	if modTime.Sub(expectedModTime) > time.Second || expectedModTime.Sub(modTime) > time.Second {
		t.Errorf("getFileTimes modTime = %v, want %v", modTime, expectedModTime)
	}
	// While accessTime may vary by system, ensure it is not the zero value.
	if accessTime.IsZero() {
		t.Error("getFileTimes returned zero accessTime")
	}
}

// TestDeleteFiles creates temporary files, deletes them via deleteFiles, and verifies they no longer exist.
func TestDeleteFiles(t *testing.T) {
	// Create temporary parent file.
	parentFile, err := os.CreateTemp("", "parentfile")
	if err != nil {
		t.Fatalf("Failed to create parent file: %v", err)
	}
	parentFileName := parentFile.Name()
	parentFile.Close()
	// Create temporary child file.
	childFile, err := os.CreateTemp("", "childfile")
	if err != nil {
		t.Fatalf("Failed to create child file: %v", err)
	}
	childFileName := childFile.Name()
	childFile.Close()

	files := []*FileInfo{
		{
			Path: parentFileName,
			Children: []*FileInfo{
				{Path: childFileName},
			},
		},
	}

	if err := deleteFiles(files); err != nil {
		t.Fatalf("deleteFiles() error: %v", err)
	}

	if _, err := os.Stat(parentFileName); !os.IsNotExist(err) {
		t.Errorf("Parent file %s still exists after deletion", parentFileName)
	}
	if _, err := os.Stat(childFileName); !os.IsNotExist(err) {
		t.Errorf("Child file %s still exists after deletion", childFileName)
	}
}
