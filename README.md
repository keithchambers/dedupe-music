# dedupe-music

dedupe-music is a command-line tool for scanning directories to identify audio files that are duplicates based on their MD5 hash. The tool can also detect files that may not have identical hashes but share the same size and similar filenames. It outputs the results in a structured JSON format, allowing users to easily see unique files and their duplicates.

## Features

- **Directory Scanning:** Scan multiple directories for audio files.
- **Duplicate Detection:** Identify duplicates based on MD5 hash, file size, and filename similarity.
- **Output Format:** Generate a JSON file that structures unique files as parent objects and their duplicates as child objects.
- **File Copying:** Optionally copy unique files to a specified directory.
- **File Deletion:** Options to delete all files or just duplicate files listed in the JSON output.
- **Logging:** Enable logging to the console for better visibility of operations.

## Requirements

- Go 1.23 or later
- Access to the directories you wish to scan

## Installation

To install the tool, clone the repository and build the binary:

```bash
git clone https://github.com/yourusername/dedupe-music.git
cd dedupe-music
go build -o dedupe-music dedupe-music.go
```

## Known issues

- MacOS only
