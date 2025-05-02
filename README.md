# yank

[![Go Report Card](https://goreportcard.com/badge/github.com/lian/yank)](https://goreportcard.com/report/github.com/lian/yank)
Yank is a terminal-based utility written in Go for interactively selecting files within a directory (and its subdirectories) and copying their contents, along with metadata, to the system clipboard. It features persistent selections per directory and fuzzy filtering for quickly finding files.

## Features

* **Interactive TUI:** Select files easily using a terminal interface powered by [Bubble Tea](https://github.com/charmbracelet/bubbletea).
* **Recursive Scanning:** Finds files in the target directory and all subdirectories.
* **Fuzzy Filtering:** Quickly search and filter the file list using [fuzzysearch](https://github.com/lithammer/fuzzysearch).
* **Multi-File Selection:** Select multiple files for copying.
* **Hidden File Toggling:** Show or hide files and directories starting with a dot (`.`). Selected hidden files always remain visible.
* **Persistence:** Remembers your last selection for each scanned directory in a hidden `.yank` file within that directory.
* **Rich Clipboard Content:** Copies not just the file content, but also metadata (relative path, modification time, size) in a structured header format.
* **Intelligent Exclusions:** Automatically ignores `.git` directories and the root `.yank` persistence file.
* **Cross-Platform Clipboard:** Works on macOS (`pbcopy`), Linux (`xclip` or `xsel`), and Windows (`clip.exe`).

## Installation

### Prerequisites

* **Go:** Version 1.21 or higher.
* **Linux:** Requires either `xclip` or `xsel` to be installed for clipboard functionality.
    * Debian/Ubuntu: `sudo apt update && sudo apt install xclip`
    * Fedora: `sudo dnf install xclip`
    * Arch: `sudo pacman -S xclip`

### Using `go install`

```bash
go install github.com/lian/yank@latest
```

This will download the source code, compile it, and place the `yank` binary in your `$GOPATH/bin` or `$HOME/go/bin` directory (ensure this is in your system's `PATH`).

### From Source

```bash
# Replace with the actual repo URL if different
git clone https://github.com/lian/yank.git
cd yank
go build -o yank .
# Optionally, move the 'yank' binary to a directory in your PATH
# sudo mv ./yank /usr/local/bin/
```

## Usage

### Command Line

Run `yank` in the directory you want to scan, or provide a path using the `-dir` flag:

```bash
# Scan the current directory
yank

# Scan a specific directory
yank -dir /path/to/your/project

# Show help message
yank -h
# or
yank --help

```

### TUI Keybindings

Once the TUI is running:

**Normal Mode:**

| Key(s) | Action |
 | ----- | ----- |
| `j`, `k`, `↓`, `↑` | Move cursor up/down. |
| `space`, `m` | Toggle selection for the focused file/path. |
| `c`, `C` | Clear all selected files. |
| `.` | Toggle visibility of hidden files/directories (starting with `.`). |
| `/` | Enter filter mode (fuzzy search). |
| `y`, `enter` | Confirm selection, copy data to clipboard, save selection, and quit. |
| `q`, `ctrl+c` | Quit without copying or saving the current selection state. |
| `?` | Show/hide the full help view for more keys (like PgUp/PgDn). |

**Filter Mode (after pressing `/`):**

| Key(s) | Action |
 | ----- | ----- |
| *(type text)* | Enter text to fuzzy filter the list. |
| `esc` | Exit filter mode and clear the current filter. |
| `ctrl+j` | Move cursor down within the filtered list. |
| `ctrl+k` | Move cursor up within the filtered list. |
| `ctrl+m` | Toggle selection for the focused file (works on the underlying selection). |
| `backspace` | Delete the last character from the filter query. |
| `y`, `enter` | Confirm selection (uses *all* selected files), copy, save, and quit. |
| `q`, `ctrl+c` | Quit without copying or saving. |

## Clipboard Format

When you confirm your selection, the content of each selected file is copied to the clipboard, preceded by a header containing metadata:

```
--- FILENAME: relative/path/to/your/file.txt | Modified: 2025-05-02 17:18:10 | Size: 47004 bytes ---
(Content of file.txt goes here...)


--- FILENAME: another/subdir/code.go | Modified: 2025-05-01 10:30:00 | Size: 1024 bytes ---
(Content of code.go goes here...)



```

## Persistence

Yank saves the relative paths of your selected files in a hidden file named `.yank` within the root of the directory you scanned.

* When you start `yank` in a directory containing a `.yank` file, your previous selection is automatically loaded and checked against the currently available files.

* When you confirm a selection (`y`/`enter`), the `.yank` file is updated with the current selection.

* If you confirm with *no* files selected (or clear the selection and then confirm), the `.yank` file is removed.

## Dependencies

* **Runtime:**

  * Linux: `xclip` or `xsel` for clipboard access.

* **Go Modules:**

  * [github.com/charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) (TUI Framework)

  * [github.com/charmbracelet/bubbles](https://github.com/charmbracelet/bubbles) (TUI Components like list, key)

  * [github.com/charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss) (TUI Styling)

  * [github.com/lithammer/fuzzysearch](https://github.com/lithammer/fuzzysearch) (Fuzzy Searching)

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Acknowledgements

* The wonderful TUI libraries provided by [Charm](https://charm.sh/).

* The efficient fuzzy searching library by [lithammer](https://github.com/lithammer)
