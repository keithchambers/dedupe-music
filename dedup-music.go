package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// DirList is a custom type for a list of directories.
type DirList []string

func (d *DirList) String() string {
	return strings.Join(*d, ", ")
}

func (d *DirList) Set(value string) error {
	*d = append(*d, value)
	return nil
}

// FileInfo holds information about a file, including its path, hash, size, and duplicates.
type FileInfo struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Hash     string      `json:"hash"`
	Size     int64       `json:"size"`
	Children []*FileInfo `json:"duplicates,omitempty"`
}

var (
	sourceDirs         DirList
	targetDir          string
	minSizeMB          int64
	logEnabled         bool
	deleteSourceFiles  bool
	numWorkers         = runtime.NumCPU()
)

func init() {
	flag.Var(&sourceDirs, "s", "Directory to scan for files to be deduped. Can be used multiple times. (Required)")
	flag.Var(&sourceDirs, "source-dir", "Directory to scan for files to be deduped. Can be used multiple times. (Required)")
	flag.StringVar(&targetDir, "t", "", "Directory to copy unique files to. (Optional)")
	flag.StringVar(&targetDir, "target-dir", "", "Directory to copy unique files to. (Optional)")
	flag.Int64Var(&minSizeMB, "size", 10, "Minimum file size in megabytes (MB) to consider. (Optional, default: 10)")
	flag.BoolVar(&deleteSourceFiles, "delete-source-files", false, "Delete source files after processing. (Optional, default: false)")
	flag.BoolVar(&logEnabled, "l", false, "Enable detailed logging to the console. (Optional, default: false)")
	flag.BoolVar(&logEnabled, "logs", false, "Enable detailed logging to the console. (Optional, default: false)")
	flag.Usage = customUsage
}

func customUsage() {
	fmt.Fprintf(os.Stderr, "Dedupe Music: Find and manage duplicate files\n\n")
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  dedupe-music [options]\n\n")
	fmt.Fprintf(os.Stderr, "Options:\n")
	fmt.Fprintf(os.Stderr, "  -s, -source-dir value\n")
	fmt.Fprintf(os.Stderr, "      Directory to scan for files to be deduped. Can be used multiple times. (Required)\n")
	fmt.Fprintf(os.Stderr, "      Example: -s \"$HOME/Music/\" -s \"$HOME/Downloads/\"\n\n")
	fmt.Fprintf(os.Stderr, "  -t, -target-dir string\n")
	fmt.Fprintf(os.Stderr, "      Directory to copy unique files to. (Optional)\n")
	fmt.Fprintf(os.Stderr, "      Example: -t \"$HOME/deduped-files-dir\"\n\n")
	fmt.Fprintf(os.Stderr, "  -size value\n")
	fmt.Fprintf(os.Stderr, "      Minimum file size in megabytes (MB) to consider. (Optional, default: 10)\n")
	fmt.Fprintf(os.Stderr, "      Example: -size 5\n\n")
	fmt.Fprintf(os.Stderr, "  -delete-source-files\n")
	fmt.Fprintf(os.Stderr, "      Delete source files after processing. (Optional, default: false)\n")
	fmt.Fprintf(os.Stderr, "      WARNING: Use with caution! This will delete files!\n\n")
	fmt.Fprintf(os.Stderr, "  -l, -logs\n")
	fmt.Fprintf(os.Stderr, "      Enable detailed logging to the console. (Optional, default: false)\n\n")
	fmt.Fprintf(os.Stderr, "  -h, -help\n")
	fmt.Fprintf(os.Stderr, "      Show this help message\n\n")
}

func main() {
	flag.Parse()
	if len(os.Args) == 1 || containsHelpFlag() {
		flag.Usage()
		os.Exit(0)
	}
	if len(sourceDirs) == 0 {
		fmt.Fprintf(os.Stderr, "Error: Source (-s or -source-dir) directories are required.\n")
		flag.Usage()
		os.Exit(1)
	}
	if deleteSourceFiles {
		fmt.Print("Enter the word 'permanent' and hit enter to confirm: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input != "permanent" {
			fmt.Fprintf(os.Stderr, "Error: Deletion not confirmed. Exiting.\n")
			os.Exit(1)
		}
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// containsHelpFlag checks for -h or -help in the command-line arguments.
func containsHelpFlag() bool {
	for _, arg := range os.Args[1:] {
		if arg == "-h" || arg == "-help" || arg == "--help" {
			return true
		}
	}
	return false
}

func run() error {
	logf("Starting dedupe-music program")
	outputFile := "dedupe-music.json"

	if targetDir != "" {
		if err := os.MkdirAll(targetDir, os.ModePerm); err != nil {
			return fmt.Errorf("error creating output directory %s: %v", targetDir, err)
		}
		logf("Output directory created or exists: %s", targetDir)
	}

	minSizeBytes := minSizeMB * 1024 * 1024
	// Allowed audio file extensions.
	allowedExts := map[string]bool{
		".wav":  true,
		".aif":  true,
		".aiff": true,
		".mp3":  true,
	}

	// Map to hold unique files based on a composite key.
	fileMap := make(map[string]*FileInfo)
	var fileMapMutex sync.Mutex
	fileChan := make(chan string, 100)
	var wg sync.WaitGroup

	// Start worker pool.
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker(fileChan, &fileMap, &fileMapMutex, &wg)
	}

	// Walk through each source directory.
	for _, dir := range sourceDirs {
		logf("Scanning directory: %s", dir)
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				// Skip permission errors.
				if errors.Is(err, os.ErrPermission) {
					return nil
				}
				return fmt.Errorf("error accessing %s: %v", path, err)
			}
			// Only process regular files that meet the minimum size.
			if !info.Mode().IsRegular() || info.Size() < minSizeBytes {
				return nil
			}
			// Check if file extension is allowed.
			ext := strings.ToLower(filepath.Ext(info.Name()))
			if allowedExts[ext] {
				fileChan <- path
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("error walking directory %s: %v", dir, err)
		}
	}

	close(fileChan)
	wg.Wait()

	// Process the collected file information.
	var output []*FileInfo
	for _, fileInfo := range fileMap {
		output = append(output, fileInfo)
		if targetDir != "" {
			logf("Copying file: %s", fileInfo.Path)
			if err := copyFile(fileInfo.Path, targetDir, fileInfo); err != nil {
				return fmt.Errorf("error copying file %s: %v", fileInfo.Path, err)
			}
			logf("Successfully copied file: %s", fileInfo.Path)
		}
	}

	if deleteSourceFiles {
		if err := deleteFiles(output); err != nil {
			return fmt.Errorf("error deleting files: %v", err)
		}
	}

	if err := writeJSONToFile(outputFile, output); err != nil {
		return fmt.Errorf("error writing JSON to file: %v", err)
	}

	fmt.Printf("Results written to %s\n", outputFile)
	if targetDir != "" {
		fmt.Printf("Files copied to %s\n", targetDir)
	}

	return nil
}

// worker processes file paths from fileChan, computes their hash, and updates fileMap.
func worker(fileChan <-chan string, fileMap *map[string]*FileInfo, fileMapMutex *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()
	for path := range fileChan {
		logf("Processing file: %s", path)
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to stat file %s: %v\n", path, err)
			continue
		}
		size := info.Size()
		hash, err := fileHash(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Unable to hash file %s: %v\n", path, err)
			continue
		}
		filename := filepath.Base(path)
		fileInfo := &FileInfo{
			Name: filename,
			Path: path,
			Hash: hash,
			Size: size,
		}
		// Generate a composite key based on filename and hash.
		key := generateKey(filename, hash)
		fileMapMutex.Lock()
		if existingFile, exists := (*fileMap)[key]; exists {
			existingFile.Children = append(existingFile.Children, fileInfo)
		} else {
			(*fileMap)[key] = fileInfo
		}
		fileMapMutex.Unlock()
	}
}

// generateKey creates a deduplication key from filename and hash.
func generateKey(filename, hash string) string {
	return filename + "|" + hash
}

// fileHash computes the MD5 hash of the file at the given path.
func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := md5.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// writeJSONToFile encodes the data to JSON and writes it to the specified file.
func writeJSONToFile(filename string, data []*FileInfo) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", " ")
	return encoder.Encode(data)
}

// copyFile copies a file from srcPath to destDir, ensuring that it does not overwrite existing files.
func copyFile(srcPath, destDir string, fileInfo *FileInfo) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	filename := filepath.Base(srcPath)
	destPath := filepath.Join(destDir, filename)
	i := 1
	for {
		if _, err := os.Stat(destPath); os.IsNotExist(err) {
			break
		}
		destPath = filepath.Join(destDir, fmt.Sprintf("%s(%d)%s",
			strings.TrimSuffix(filename, filepath.Ext(filename)), i, filepath.Ext(filename)))
		i++
	}

	destFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, srcFile); err != nil {
		return err
	}

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	if err := os.Chmod(destPath, info.Mode()); err != nil {
		return err
	}

	atime, mtime, err := getFileTimes(srcPath)
	if err != nil {
		return err
	}
	return os.Chtimes(destPath, atime, mtime)
}

// getFileTimes retrieves the access and modification times of the file at the given path.
func getFileTimes(path string) (accessTime, modTime time.Time, err error) {
	var stat unix.Stat_t
	if err = unix.Stat(path, &stat); err != nil {
		return
	}
	accessTime = time.Unix(int64(stat.Atim.Sec), int64(stat.Atim.Nsec))
	modTime = time.Unix(int64(stat.Mtim.Sec), int64(stat.Mtim.Nsec))
	return
}

// deleteFiles removes the parent file and all its duplicates.
func deleteFiles(files []*FileInfo) error {
	for _, fileInfo := range files {
		if err := removeFile(fileInfo.Path); err != nil {
			return fmt.Errorf("error deleting file %s: %v", fileInfo.Path, err)
		}
		for _, child := range fileInfo.Children {
			if err := removeFile(child.Path); err != nil {
				return fmt.Errorf("error deleting file %s: %v", child.Path, err)
			}
		}
	}
	logf("Source files deleted")
	return nil
}

// removeFile wraps os.RemoveAll to delete a file or directory.
func removeFile(path string) error {
	return os.RemoveAll(path)
}

// logf prints log messages with a timestamp if logging is enabled.
func logf(format string, args ...interface{}) {
	if logEnabled {
		prefix := time.Now().Format("2006-01-02 15:04:05")
		fmt.Printf("%s: %s\n", prefix, fmt.Sprintf(format, args...))
	}
}
