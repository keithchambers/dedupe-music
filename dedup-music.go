package main

import (
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

	"golang.org/x/sys/unix" // For Unix-specific system calls
)

// DirList is a custom type for a list of directories.
type DirList []string

// String converts DirList to a comma-separated string.
func (d *DirList) String() string {
	return strings.Join(*d, ", ")
}

// Set appends a new directory to DirList.
func (d *DirList) Set(value string) error {
	*d = append(*d, value)
	return nil
}

// FileInfo holds information about a file, including its path, hash, size, and duplicates.
type FileInfo struct {
	Path     string      `json:"path"`               // File path
	Hash     string      `json:"hash,omitempty"`     // MD5 hash
	Size     int64       `json:"size"`               // File size in bytes
	Children []*FileInfo `json:"duplicates,omitempty"` // Duplicates as children
}

var (
	dirFlags    DirList // Directories to scan
	helpFlag    bool    // Help flag
	outputFile  string  // Output JSON file name
	copyDir     string  // Directory to copy unique files to
	minSizeMB   int64   // Minimum file size in MB
	logEnabled  bool    // Enable logging to the console
	deleteAll   bool    // Flag to delete all files in JSON
	deleteDupes bool    // Flag to delete only duplicate files
	numWorkers  = runtime.NumCPU() // Number of worker goroutines to use
)

// init initializes command-line flags.
func init() {
	flag.Var(&dirFlags, "d", "Directory to scan (can be used multiple times)")
	flag.BoolVar(&helpFlag, "h", false, "Show help")
	flag.StringVar(&outputFile, "f", "", "Output JSON file (optional)")
	flag.StringVar(&copyDir, "o", "", "Output directory to copy unique files to (optional)")
	flag.Int64Var(&minSizeMB, "s", 0, "Minimum file size in MB (optional)")
	flag.BoolVar(&logEnabled, "l", false, "Enable logging to the console (optional)")
	flag.BoolVar(&deleteAll, "delete-all", false, "Delete all files listed in the JSON")
	flag.BoolVar(&deleteDupes, "delete-duplicates", false, "Delete only duplicate files listed in the JSON")
}

// log prints a message to the console if logging is enabled.
func log(msg string, args ...interface{}) {
	if logEnabled {
		fmt.Printf(msg+"\n", args...)
	}
}

// main is the entry point of the program.
func main() {
	flag.Parse() // Parse command-line flags

	// Display help if requested or no directories are specified
	if helpFlag || len(dirFlags) == 0 {
		flag.Usage()
		return
	}

	log("Starting dedupe-music program")

	// Set default output file name if not provided
	if outputFile == "" {
		timestamp := time.Now().Format("01-02-2006_15-04-05")
		outputFile = fmt.Sprintf("dedupe-music_%s.json", timestamp)
	}

	// Create the output directory if specified
	if copyDir != "" {
		err := os.MkdirAll(copyDir, os.ModePerm)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating output directory %s: %v\n", copyDir, err)
			return
		}
		log("Output directory created or exists: %s", copyDir)
	}

	// Convert minimum size from MB to bytes
	minSizeBytes := minSizeMB * 1024 * 1024

	// Supported audio file extensions
	audioExtensions := map[string]bool{
		".wav":  true,
		".aif":  true,
		".aiff": true,
		".mp3":  true,
		".mp4":  true,
	}

	// Map to hold file hashes and their information
	fileMap := make(map[string]*FileInfo)
	var fileMapMutex sync.Mutex // Mutex for synchronizing access to fileMap

	fileChan := make(chan string, 100) // Channel for file paths
	var wg sync.WaitGroup                // WaitGroup to wait for all workers to finish

	// Start worker goroutines
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker(fileChan, audioExtensions, fileMap, &fileMapMutex, &wg)
	}

	// Walk through each directory provided via the -d flag
	for _, dir := range dirFlags {
		log("Scanning directory: %s", dir)
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrPermission) {
					return nil // Skip permission errors
				}
				fmt.Fprintf(os.Stderr, "Error accessing %s: %v\n", path, err)
				return nil
			}

			// Skip non-regular files and files smaller than the minimum size
			if !info.Mode().IsRegular() || info.Size() < minSizeBytes {
				return nil
			}

			// Check if the file has a supported audio extension
			ext := strings.ToLower(filepath.Ext(info.Name()))
			if audioExtensions[ext] {
				fileChan <- path // Send file path to channel for processing
			}
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error walking directory %s: %v\n", dir, err)
		}
	}

	close(fileChan) // Close the channel after processing all directories
	wg.Wait()       // Wait for all worker goroutines to finish

	var output []*FileInfo // Prepare the output data

	// Prepare the output data and handle file copying
	for _, fileInfo := range fileMap {
		output = append(output, fileInfo) // Add unique file to output
		if copyDir != "" {
			log("Copying file: %s", fileInfo.Path)
			err := copyFile(fileInfo.Path, copyDir, fileInfo)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error copying file %s: %v\n", fileInfo.Path, err)
				return // Exit if any copy fails
			} else {
				log("Successfully copied file: %s", fileInfo.Path)
			}
		}
	}

	// Handle deletion if requested
	if deleteAll || deleteDupes {
		if err := deleteFiles(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting files: %v\n", err)
			return
		}
	}

	// Write the output data to a JSON file
	if err := writeJSONToFile(outputFile, output); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing JSON to file: %v\n", err)
		return
	}

	fmt.Printf("Results written to %s\n", outputFile)

	if copyDir != "" {
		fmt.Printf("Files copied to %s\n", copyDir)
	}
}

// worker processes file paths from the fileChan and populates the fileMap
func worker(fileChan <-chan string, audioExtensions map[string]bool, fileMap map[string]*FileInfo, fileMapMutex *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done() // Signal that this worker is done when the function exits

	for path := range fileChan {
		log("Processing file: %s", path)

		info, err := os.Stat(path) // Get file information
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error stating file %s: %v\n", path, err)
			continue
		}

		size := info.Size() // Get file size
		hash, err := fileHash(path) // Compute the MD5 hash
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error hashing %s: %v\n", path, err)
			continue
		}

		fileInfo := &FileInfo{
			Path: path,
			Hash: hash,
			Size: size,
		}

		// Lock the mutex to update the shared fileMap
		fileMapMutex.Lock()
		if existingFile, exists := fileMap[hash]; exists {
			// If the file already exists, append it as a duplicate of the unique file
			existingFile.Children = append(existingFile.Children, fileInfo)
		} else {
			// Otherwise, add it as a new unique entry
			fileMap[hash] = fileInfo
		}
		fileMapMutex.Unlock()
	}

	// Additional duplicate detection based on size and filename similarity
	fileMapMutex.Lock()
	defer fileMapMutex.Unlock()

	hashes := make([]string, 0, len(fileMap))
	for hash := range fileMap {
		hashes = append(hashes, hash)
	}

	// Compare files for additional duplicates
	for i := 0; i < len(hashes); i++ {
		fileA := fileMap[hashes[i]]
		for j := i + 1; j < len(hashes); j++ {
			fileB := fileMap[hashes[j]]
			if fileA.Size == fileB.Size {
				// Check for filename similarity
				similarity := compareFilenames(filepath.Base(fileA.Path), filepath.Base(fileB.Path))
				if similarity >= 0.5 {
					// Add fileB as a child of fileA
					fileA.Children = append(fileA.Children, fileB)
					delete(fileMap, hashes[j]) // Remove duplicate from map
					hashes = append(hashes[:j], hashes[j+1:]...) // Remove from the slice
					j-- // Adjust index due to removal
				}
			}
		}
	}
}

// fileHash calculates the MD5 hash of the file at the given path
func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := md5.New()
	_, err = io.Copy(hasher, file)
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// writeJSONToFile writes the provided data as JSON to the specified filename
func writeJSONToFile(filename string, data []*FileInfo) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// copyFile copies a file from srcPath to the destDir
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
		destPath = filepath.Join(destDir, fmt.Sprintf("%s(%d)%s", strings.TrimSuffix(filename, filepath.Ext(filename)), i, filepath.Ext(filename)))
		i++
	}

	destFile, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, srcFile)
	if err != nil {
		return err
	}

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	err = os.Chmod(destPath, info.Mode())
	if err != nil {
		return err
	}

	atime, mtime, err := getFileTimes(srcPath)
	if err != nil {
		return err
	}
	return os.Chtimes(destPath, atime, mtime)
}

// getFileTimes retrieves the access and modification times of a file
func getFileTimes(path string) (accessTime, modTime time.Time, err error) {
	var stat unix.Stat_t
	err = unix.Stat(path, &stat)
	if err != nil {
		return
	}

	accessTime = time.Unix(int64(stat.Atim.Sec), int64(stat.Atim.Nsec))
	modTime = time.Unix(int64(stat.Mtim.Sec), int64(stat.Mtim.Nsec))
	return
}

// compareFilenames compares two filenames and returns a similarity score
func compareFilenames(name1, name2 string) float64 {
	name1Letters := getLetters(name1)
	name2Letters := getLetters(name2)

	matches := 0
	for _, c := range name1Letters {
		if strings.ContainsRune(name2Letters, c) {
			matches++
		}
	}

	longerLength := len(name1Letters)
	if len(name2Letters) > longerLength {
		longerLength = len(name2Letters)
	}

	if longerLength == 0 {
		return 0
	}

	return float64(matches) / float64(longerLength)
}

// getLetters extracts letters from a string, ignoring certain characters
func getLetters(s string) string {
	var letters []rune
	for _, r := range s {
		if r != ' ' && r != '_' && r != '-' && r != '.' {
			letters = append(letters, r)
		}
	}
	return string(letters)
}

// deleteFiles deletes files based on the provided output structure
func deleteFiles(output []*FileInfo) error {
	var err error

	if deleteAll {
		for _, fileInfo := range output {
			err = os.RemoveAll(fileInfo.Path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error deleting file %s: %v\n", fileInfo.Path, err)
			}
		}
		log("All files deleted.")
	} else if deleteDupes {
		for _, fileInfo := range output {
			for _, child := range fileInfo.Children {
				err = os.Remove(child.Path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error deleting duplicate file %s: %v\n", child.Path, err)
				}
			}
		}
		log("Duplicate files deleted.")
	}

	return err
}
