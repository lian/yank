package main

// Go module path: github.com/lian/yank

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lithammer/fuzzysearch/fuzzy"
)

const (
	// persistenceDotFileName defines the name of the hidden file used to store selections within the target directory.
	persistenceDotFileName = ".yank"
	appName                = "yank"
)

var (
	// Define styles using lipgloss for UI elements. Reusable styles improve consistency.
	docStyle          = lipgloss.NewStyle().Margin(1, 2)
	titleStyle        = lipgloss.NewStyle().MarginLeft(0).Bold(true).Foreground(lipgloss.Color("62"))
	itemStyle         = lipgloss.NewStyle().PaddingLeft(0)
	selectedStyle     = lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.Color("75")).Bold(true)
	checkedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	helpStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errorStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	filterPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
)

// --- Bubble Tea Model ---

// item represents a single file entry in the list component.
// It implements the list.Item interface necessary for bubbles/list.
type item struct {
	name string // Stores the relative path of the file from the target directory
}

// Title is required by the list.Item interface. Returns the text to display for the item.
func (i item) Title() string { return i.name }

// Description is required by list.Item. Returns secondary text (not used here).
func (i item) Description() string { return "" }

// FilterValue is required by list.Item. Returns the string value used for filtering.
func (i item) FilterValue() string { return i.name }

// model holds the entire state of the TUI application during its lifecycle.
type model struct {
	targetDir         string          // The root directory being scanned (absolute path).
	list              list.Model      // The bubbletea list component managing the file list UI.
	selected          map[string]bool // Tracks selection state (key: relative path, value: true if selected).
	keys              keyMap          // Defines the application's keybindings.
	err               error           // Stores runtime errors to display to the user instead of the list.
	quitting          bool            // Flag set when the user initiates shutdown (e.g., presses 'q').
	copyStarted       bool            // Flag set when the async copy/save process begins, prevents other actions.
	showHidden        bool            // Flag indicating whether paths containing dot-prefixed components should be displayed.
	allAvailableFiles []string        // Slice storing all relative file paths found during the initial scan.
	statusMessage     string          // Temporary status messages displayed below the list.
	statusTimer       *time.Timer     // Timer used to clear the status message after a delay.
	isFiltering       bool            // Flag indicating if search/filter mode is active.
	filterQuery       string          // Stores the current user-entered search query.
}

// --- Keybindings ---

// keyMap defines the keybindings used by the application, utilizing bubbles/key
// for easy definition and display in help messages.
type keyMap struct {
	Toggle        key.Binding // Toggles selection for the focused item (space, m).
	Confirm       key.Binding // Confirms selection, copies data, saves state, and quits (y, enter).
	Quit          key.Binding // Quits the application without copying (q, ctrl+c).
	ToggleHidden  key.Binding // Toggles visibility of hidden paths (.).
	StartFilter   key.Binding // Key to activate filter mode (/).
	ClearFilter   key.Binding // Key to clear filter query and exit filter mode (esc).
	ClearSelected key.Binding // Key to clear selected files.
	// NOTE: Ctrl+J, Ctrl+K, Ctrl+M for filter-mode actions are handled directly via msg.Type in Update.
}

// defaultKeyMap returns the standard key configuration for the application.
func defaultKeyMap() keyMap {
	return keyMap{
		Toggle: key.NewBinding(
			key.WithKeys(" ", "m"),
			key.WithHelp("space/m", "toggle select"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("y", "enter"),
			key.WithHelp("y/enter", "copy & quit"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c", "ctrl+q"),
			key.WithHelp("q/ctrl+c", "quit"),
		),
		ToggleHidden: key.NewBinding(
			key.WithKeys("."),
			key.WithHelp(".", "toggle hidden paths"),
		),
		StartFilter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter list"),
		),
		ClearFilter: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "clear filter"),
		),
		ClearSelected: key.NewBinding(
			key.WithKeys("c", "C"),
			key.WithHelp("c/C", "clear selected"),
		),
	}
}

// --- Status Message Handling ---

// clearStatusMsg is a custom tea.Msg used internally to signal that
// the temporary status message should be cleared from the view.
type clearStatusMsg struct{}

// clearStatusCmd returns a tea.Cmd (a function that returns a tea.Msg)
// that waits for a specified duration 'd' and then sends a clearStatusMsg
// back to the application's Update loop.
func clearStatusCmd(d time.Duration) tea.Cmd {
	// This command function runs in its own goroutine managed by Bubble Tea.
	return func() tea.Msg {
		time.Sleep(d)
		return clearStatusMsg{}
	}
}

// --- Model Methods ---

// initialModel sets up the initial state of the application model.
// It takes the absolute path of the target directory as input.
func initialModel(targetDir string) model {
	m := model{
		targetDir:   targetDir,
		selected:    make(map[string]bool),
		keys:        defaultKeyMap(),
		showHidden:  false,
		isFiltering: false,
		filterQuery: "",
	}

	// --- Load Files and Selection State ---
	// Perform the recursive file scan and load previous selections from the .yank file.
	allFiles, previouslySelectedFiles, err := loadFilesAndSelectionRecursive(targetDir)
	if err != nil {
		// If loading fails (e.g., cannot read target directory), store the error.
		// The View method will detect this error and display it instead of the list.
		m.err = fmt.Errorf("failed initial load: %w", err)
		// Ensure the slice is initialized even on error
		m.allAvailableFiles = []string{}
	} else {
		m.allAvailableFiles = allFiles
	}

	// Populate the selection map based on data loaded from the .yank file.
	for _, selRelativePath := range previouslySelectedFiles {
		m.selected[selRelativePath] = true
	}

	// --- Setup the bubbles/list Component ---
	delegate := newItemDelegate(&m.selected)     // Create our custom delegate for rendering items
	l := list.New([]list.Item{}, delegate, 0, 0) // Initialize list with empty items (populated by refreshListItems)
	l.Styles.Title = titleStyle
	// Define which keybindings are shown in the full help view ('?'), dynamically
	// changing based on whether the user is currently filtering.
	l.AdditionalFullHelpKeys = func() []key.Binding {
		if m.isFiltering { // When filtering, only show relevant keys.
			// Note: Ctrl+J/K/M aren't easily shown here as they aren't standard Bindings.
			return []key.Binding{m.keys.ClearFilter, m.keys.Confirm, m.keys.Quit}
		}
		// When not filtering, show the main action keys.
		return []key.Binding{m.keys.Toggle, m.keys.ToggleHidden, m.keys.StartFilter, m.keys.Confirm, m.keys.Quit, m.keys.ClearSelected}
	}
	// Configure list appearance and behavior.
	l.SetShowStatusBar(false)    // We handle status messages separately below the list.
	l.SetFilteringEnabled(false) // Disable the list's built-in filtering; we implement our own fuzzy search.
	l.SetShowHelp(true)          // Enable the default help view feature (toggled by '?').

	m.list = l
	m.refreshListItems() // Perform the initial population of list items based on loaded state.

	return m
}

// refreshListItems filters `allAvailableFiles` based on the `showHidden` flag and
// the `selected` map (specifically, showing selected hidden items), then updates
// the items displayed in the list component. Called when not filtering or clearing filter.
func (m *model) refreshListItems() {
	var visibleItems []list.Item

	// Iterate through all files found during the initial scan.
	for _, relativePath := range m.allAvailableFiles {

		// Check if the path contains a hidden component (directory or file starting with '.')
		pathContainsHidden := false
		parts := strings.Split(relativePath, string(os.PathSeparator))
		for _, part := range parts {
			// Check if a component starts with "." but isn't just "." or "..".
			if strings.HasPrefix(part, ".") && part != "." && part != ".." {
				pathContainsHidden = true
				break
			}
		}

		isSelected := m.selected[relativePath]

		// --- Visibility Logic ---
		// Determine if this item should be visible in the list based on current state:
		// Show if:
		// 1. Its path does NOT contain any hidden component, OR
		// 2. The global 'showHidden' flag is currently true, OR
		// 3. The item itself is selected (selected items bypass the hidden toggle).
		if !pathContainsHidden || m.showHidden || isSelected {
			visibleItems = append(visibleItems, item{name: relativePath})
		}
	}

	// --- Preserve Cursor Position ---
	// Try to keep the cursor focused on the same item after the list content changes.
	var currentRelativePath string
	// Get the relative path of the currently focused item *before* updating the list.
	// Check list bounds first to avoid panic on empty list or invalid index.
	if len(m.list.Items()) > 0 && m.list.Index() >= 0 {
		// Check item type before accessing name
		if currentItem, ok := m.list.SelectedItem().(item); ok {
			currentRelativePath = currentItem.name
		}
	}

	m.list.SetItems(visibleItems)

	// If we recorded a focused item, try to find it in the *new* list and restore focus.
	if currentRelativePath != "" {
		for i, listItem := range visibleItems {
			if li, ok := listItem.(item); ok && li.name == currentRelativePath {
				m.list.Select(i)
				break
			}
		}
	}

	// Set a simple, static title for the list (removed dynamic hidden counts).
	m.list.Title = "Select files:"
}

// applyFilter performs fuzzy search on allAvailableFiles using the model's filterQuery
// and updates the list component's items with the ranked results.
// If the query is empty, it reverts to the normal (potentially hidden-filtered) view
// by calling refreshListItems.
func (m *model) applyFilter() {
	// If the filter query is empty, restore the default filtered view.
	if m.filterQuery == "" {
		m.list.Title = fmt.Sprintf("Filter results for '%s':", m.filterQuery)
		m.refreshListItems() // Shows items respecting showHidden and selected state.
		return
	}

	// Perform case-insensitive fuzzy search using the imported library.
	// RankFindFold finds matches and provides ranks (based on edit distance, etc.).
	ranks := fuzzy.RankFindFold(m.filterQuery, m.allAvailableFiles)

	// Sort the ranks: Best matches (lowest distance, highest score) come first.
	sort.Sort(ranks)

	var filteredItems []list.Item
	for _, r := range ranks {
		// r.Target holds the original string (relative path) that matched.
		filteredItems = append(filteredItems, item{name: r.Target})
	}

	m.list.SetItems(filteredItems)
	m.list.Title = fmt.Sprintf("Filter results for '%s':", m.filterQuery)
}

// Init is the first command executed when the application starts.
// It can be used to trigger initial asynchronous operations.
func (m model) Init() tea.Cmd {
	// No initial async operations needed in this application.
	return nil
}

// Update is the core message handling function of the Bubble Tea application.
// It processes incoming messages (key presses, command results, etc.) and
// returns an updated model state and potentially new commands to execute.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// --- Global Error Handling ---
	// If an error is already set in the model, prevent further updates except quitting.
	if m.err != nil {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			// Allow quitting even when an error is displayed.
			if key.Matches(keyMsg, m.keys.Quit) {
				return m, tea.Quit
			}
		}
		// Ignore all other messages while in an error state.
		return m, nil
	}

	// --- Message Handling ---
	switch msg := msg.(type) {
	// Handle terminal resize events.
	case tea.WindowSizeMsg:
		// Recalculate list dimensions based on new window size and document margins.
		h, v := docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)

		// Handle the custom message to clear the status bar.
	case clearStatusMsg:
		m.statusMessage = ""
		if m.statusTimer != nil {
			m.statusTimer.Stop() // Ensure the associated timer is stopped.
			m.statusTimer = nil
		}

		// Handle keyboard input events.
	case tea.KeyMsg:
		// --- Global Keybindings (handle before specific modes) ---
		// Always allow quitting the application.
		if key.Matches(msg, m.keys.Quit) {
			m.quitting = true
			if m.statusTimer != nil {
				m.statusTimer.Stop() // Clean up status timer if active.
			}
			return m, tea.Quit
		}

		// Ignore regular key presses if already quitting or if the copy process has started.
		if m.quitting || m.copyStarted {
			return m, nil
		}

		// --- Filtering Mode Logic ---
		// Handle keys differently based on whether filtering is currently active.
		if m.isFiltering {
			// Handle specific keys within filter mode first, primarily using msg.Type
			// for control keys or keys handled specially during filtering.
			switch msg.Type {
			// Exit filter mode with Esc key.
			case tea.KeyEsc:
				m.isFiltering = false
				m.filterQuery = ""
				m.refreshListItems() // Restore normal list view (respecting showHidden).
				// Restore normal help key display in the full help view.
				m.list.AdditionalFullHelpKeys = func() []key.Binding {
					return []key.Binding{m.keys.Toggle, m.keys.ToggleHidden, m.keys.StartFilter, m.keys.Confirm, m.keys.Quit, m.keys.ClearSelected}
				}
				return m, nil

				// Handle backspace to delete from the filter query.
			case tea.KeyBackspace:
				if len(m.filterQuery) > 0 {
					// Use rune-aware slicing to correctly handle multi-byte characters.
					m.filterQuery = string([]rune(m.filterQuery)[:len([]rune(m.filterQuery))-1])
					m.applyFilter()
				}
				return m, nil

				// Handle Ctrl+J for navigating down in filtered list.
			case tea.KeyCtrlJ:
				m.list.CursorDown()
				return m, nil

				// Handle Ctrl+K for navigating up in filtered list.
			case tea.KeyCtrlK:
				m.list.CursorUp()
				return m, nil

				// Handle Ctrl+M for toggling selection in filtered list.
			case tea.KeyCtrlM:
				// Ensure list is not empty and an item is focused before proceeding.
				if len(m.list.Items()) > 0 && m.list.Index() >= 0 {
					if currentItem, ok := m.list.SelectedItem().(item); ok {
						// Toggle the selection state directly in the main `selected` map.
						// The list item's visual state (checkbox) is updated by the delegate reading this map.
						m.selected[currentItem.name] = !m.selected[currentItem.name]
					}
				}
				return m, nil

				// Handle printable characters (runes) and spacebar for building the filter query.
				// IMPORTANT: This case must come *after* checking specific keys like Ctrl+J/K/M.
			case tea.KeyRunes, tea.KeySpace:
				// Don't add navigation runes (j,k) to the query if they might be used for nav fallback.
				// However, since we explicitly handle Ctrl+J/K, allowing j/k here is likely fine
				// and expected if the user wants to search for filenames containing 'j' or 'k'.
				m.filterQuery += string(msg.Runes)
				m.applyFilter()
				return m, nil

				// NOTE: The explicit 'case tea.KeyEnter:' was removed to fix duplicate case error.
				// Enter is handled by the key.Matches(msg, m.keys.Confirm) check below.
			default:
				// If msg.Type didn't match special keys above, check against defined bindings.
				// This handles 'y' and 'enter' for confirmation correctly.
				if key.Matches(msg, m.keys.Confirm) {
					m.copyStarted = true
					m.statusMessage = "Processing files..."
					var filesToCopy []string
					// Confirm always uses the full selection map, regardless of current filter.
					for relativePath, selected := range m.selected {
						if selected {
							filesToCopy = append(filesToCopy, relativePath)
						}
					}
					copyCmd := m.performCopyAndSave(filesToCopy)
					cmds = append(cmds, copyCmd)
					return m, tea.Batch(cmds...)
				}
			}
			// --- End Filtering Mode Specific Key Handling ---

			// --- Not Filtering Mode Logic ---
		} else {
			switch {
			// Enter filtering mode when '/' is pressed.
			case key.Matches(msg, m.keys.StartFilter):
				m.isFiltering = true
				m.filterQuery = ""
				// Update the keys shown in the full help view.
				m.list.AdditionalFullHelpKeys = func() []key.Binding {
					// Show only Esc and Confirm/Quit in help when filtering.
					return []key.Binding{m.keys.ClearFilter, m.keys.Confirm, m.keys.Quit}
				}
				m.list.Select(0)
				// Apply empty filter initially; this updates title and prepares prompt display.
				m.applyFilter()
				m.list.Title = fmt.Sprintf("Filter results for '%s':", m.filterQuery)
				return m, nil

				// Handle normal mode selection toggle ('space' or 'm').
			case key.Matches(msg, m.keys.Toggle):
				if len(m.list.Items()) > 0 && m.list.Index() >= 0 {
					if currentItem, ok := m.list.SelectedItem().(item); ok {
						relativePath := currentItem.name
						isSelected := m.selected[relativePath]
						m.selected[relativePath] = !isSelected

						// Check if a hidden path was just deselected.
						pathContainsHidden := false
						parts := strings.Split(relativePath, string(os.PathSeparator))
						for _, part := range parts {
							if strings.HasPrefix(part, ".") && part != "." && part != ".." {
								pathContainsHidden = true
								break
							}
						}
						// If hidden path deselected while hidden paths are off, refresh list.
						if isSelected && pathContainsHidden && !m.showHidden {
							m.refreshListItems()
						}
					}
				}
				return m, nil

				// Handle toggling visibility of hidden paths ('.').
			case key.Matches(msg, m.keys.ToggleHidden):
				m.showHidden = !m.showHidden
				m.refreshListItems()
				// Set status message and timer.
				if m.showHidden {
					m.statusMessage = "Showing hidden paths"
				} else {
					m.statusMessage = "Hiding hidden paths (except selected)"
				}
				if m.statusTimer != nil {
					m.statusTimer.Stop()
				}
				timerCmd := clearStatusCmd(2 * time.Second)
				cmds = append(cmds, timerCmd)
				return m, tea.Batch(cmds...)

				// Handle clearing selection ('c' or 'C').
			case key.Matches(msg, m.keys.ClearSelected):
				clear(m.selected)
				m.refreshListItems()
				// Set status message and timer.
				m.statusMessage = "Clear Selected"
				if m.statusTimer != nil {
					m.statusTimer.Stop()
				}
				timerCmd := clearStatusCmd(2 * time.Second)
				cmds = append(cmds, timerCmd)
				return m, tea.Batch(cmds...)

				// Handle confirming selection ('y' or 'enter').
			case key.Matches(msg, m.keys.Confirm):
				m.copyStarted = true
				m.statusMessage = "Processing files..."
				var filesToCopy []string
				for relativePath, selected := range m.selected {
					if selected {
						filesToCopy = append(filesToCopy, relativePath)
					}
				}
				copyCmd := m.performCopyAndSave(filesToCopy)
				cmds = append(cmds, copyCmd)
				return m, tea.Batch(cmds...)
			}
		}
	}

	// --- List Component Update (Handles Navigation, etc.) ---
	// If the message wasn't fully handled by custom logic above (e.g., it's a navigation key
	// like arrows, pageup/down in either mode, or j/k), pass it to the list component.
	var listCmd tea.Cmd
	m.list, listCmd = m.list.Update(msg) // listCmd may contain commands (e.g., for viewport scrolling).
	cmds = append(cmds, listCmd)

	return m, tea.Batch(cmds...)
}

// View renders the application's UI based on the current model state.
func (m model) View() string {
	// If an error occurred during initialization, render the error view.
	if m.err != nil {
		errStr := errorStyle.Render(fmt.Sprintf("Error: %v", m.err))
		helpStr := helpStyle.Render("\n\nPress any key to exit.")
		return docStyle.Render(errStr + helpStr)
	}
	// If quitting, render a simple exit message.
	if m.quitting {
		return docStyle.Render("Exiting...")
	}

	// --- Prepare Info/Status/Filter Line ---
	// This line appears below the list view.
	infoLine := ""
	if m.isFiltering {
		// When filtering, show the filter prompt and current query.
		prompt := filterPromptStyle.Render("Filter: ")
		// Display query + a simulated cursor using an underscore.
		infoLine = prompt + m.filterQuery + helpStyle.Render("_")
	} else if m.copyStarted {
		// Show persistent message while copying.
		infoLine = helpStyle.Render("Processing files...")
	} else if m.statusMessage != "" {
		// Show temporary status message.
		infoLine = helpStyle.Render(m.statusMessage)
	}
	// Optionally, add default help text if no other message is present.
	if infoLine == "" {
		infoLine = helpStyle.Render("Press ? for help, / to filter")
	}

	listView := m.list.View()
	return docStyle.Render(listView + "\n" + infoLine)
}

// --- Custom List Item Delegate ---

// delegate implements list.ItemDelegate to customize how items are rendered in the list.
type delegate struct {
	selected *map[string]bool // Pointer to the model's selection map (shared state).
}

// newItemDelegate creates a new instance of our custom delegate.
func newItemDelegate(selected *map[string]bool) delegate {
	// We perform all custom rendering logic within the Render method.
	return delegate{selected: selected}
}

// Height returns the number of terminal lines a single item should occupy.
func (d delegate) Height() int { return 1 }

// Spacing returns the number of empty terminal lines between items.
func (d delegate) Spacing() int { return 0 }

// Update allows the delegate to react to messages. Not needed for simple rendering.
func (d delegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }

// Render draws a single list item row, including the selection checkbox.
func (d delegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	// Safely type assert the list item to our specific 'item' type.
	i, ok := listItem.(item)
	if !ok {
		// Should not happen if list items are always of type 'item'.
		return
	}

	relativePath := i.Title()
	isSelected := (*d.selected)[relativePath] // Check selection status via the shared map pointer.

	// Determine checkbox string and apply style if checked.
	checkbox := "[ ] "
	if isSelected {
		checkbox = checkedStyle.Render("[x] ")
	}

	line := checkbox + relativePath

	// Apply styling based on whether the item is currently focused (cursor position).
	if index == m.Index() {
		// Render the focused line using the 'selected' (meaning focused) style.
		fmt.Fprint(w, selectedStyle.Render(line))
	} else {
		fmt.Fprint(w, itemStyle.Render(line))
	}
}

// --- File Operations ---

// loadFilesAndSelectionRecursive performs a recursive directory scan starting from targetDir,
// loads the previous selection state from the persistence file (.yank),
// and validates the loaded selections against the files found.
// It ignores ".git" directories and the root persistence file itself.
func loadFilesAndSelectionRecursive(targetDir string) (availableFiles []string, selectedFiles []string, err error) {
	availableFiles = make([]string, 0)
	validFileMap := make(map[string]struct{}) // Set to efficiently track relative paths found during scan.

	// --- Recursive Directory Walk using filepath.WalkDir ---
	// WalkDir traverses the file tree rooted at targetDir, calling the provided function for each file and directory.
	walkErr := filepath.WalkDir(targetDir, func(path string, d fs.DirEntry, walkErr error) error {
		// Handle errors encountered while accessing a path (e.g., permission denied).
		if walkErr != nil {
			log.Printf("Warning: accessing path '%s': %v", path, walkErr)
			// Attempt to continue walking even if some parts are inaccessible.
			if errors.Is(walkErr, fs.ErrPermission) {
				// If permission denied on a directory, skip descending into it.
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				// Skip inaccessible files but continue the walk for siblings.
				return nil
			}
			// For other errors, log but attempt to continue the walk.
			return nil
		}

		// Don't process the starting directory itself as an entry.
		if path == targetDir {
			return nil
		}

		// --- Directory Exclusions ---
		// Skip descending into ".git" directories to avoid scanning large histories.
		if d.IsDir() && d.Name() == ".git" {
			// log.Printf("Skipping %s directory.", path) // Optional log message
			return filepath.SkipDir // Tell WalkDir not to enter this directory.
		}

		// --- Process Files ---
		// We only care about files, not directories, for the selection list.
		if !d.IsDir() {
			// Calculate the path relative to the starting target directory.
			relativePath, relErr := filepath.Rel(targetDir, path)
			if relErr != nil {
				// Log error but continue walk if relative path calculation fails.
				log.Printf("Warning: could not get relative path for '%s': %v", path, relErr)
				return nil
			}

			// Exclude the persistence file *only* if it's located directly in the target directory.
			if relativePath == persistenceDotFileName {
				// Double check it's the root one, not a file with the same name deeper
				if filepath.Dir(path) == targetDir {
					return nil
				}
			}

			availableFiles = append(availableFiles, relativePath)
			// Mark this path as found for validating saved selections later.
			validFileMap[relativePath] = struct{}{}
		}
		return nil
	})

	// Check for fatal errors returned by WalkDir itself (e.g., root directory not found).
	if walkErr != nil {
		err = fmt.Errorf("error during directory walk: %w", walkErr)
		// Return any files found before the error and the error itself.
		return availableFiles, []string{}, err
	}

	// --- Load and Validate Previous Selections ---
	persistenceFilePath := getPersistenceFilePath(targetDir) // Path to ".yank" in the root targetDir.
	content, readErr := os.ReadFile(persistenceFilePath)
	if readErr != nil {
		// If the persistence file simply doesn't exist, return successfully with no previous selections.
		if errors.Is(readErr, os.ErrNotExist) {
			return availableFiles, []string{}, nil
		}
		// Report other errors encountered while reading the persistence file.
		err = fmt.Errorf("reading persistence file '%s': %w", persistenceFilePath, readErr)
		// Return files found and the read error.
		return availableFiles, []string{}, err
	}

	// Process the content of the persistence file (one relative path per line).
	loadedLines := strings.Split(string(content), "\n")
	selectedFiles = make([]string, 0)
	for _, line := range loadedLines {
		trimmedRelativePath := strings.TrimSpace(line)
		// Only process non-empty lines.
		if trimmedRelativePath != "" {
			// Only keep selections that correspond to files actually found during the recent walk.
			// This automatically handles files that might have been deleted or moved since last run.
			if _, exists := validFileMap[trimmedRelativePath]; exists {
				selectedFiles = append(selectedFiles, trimmedRelativePath)
			} else {
				// Log if a previously selected file is no longer found.
				log.Printf("Note: Previously selected file '%s' not found during walk, removing from list.", trimmedRelativePath)
			}
		}
	}

	return availableFiles, selectedFiles, nil
}

// saveSelections saves the provided list of selected relative paths to the persistence file
// located in the target directory root. If the list is empty, it removes the file.
func saveSelections(relativePaths []string, targetDir string) error {
	filePath := getPersistenceFilePath(targetDir) // Path to ".yank" in root.

	// If the current selection is empty, remove the persistence file to clean up.
	if len(relativePaths) == 0 {
		err := os.Remove(filePath)
		// Report error only if it's *not* "file doesn't exist" (which is fine).
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed remove persistence file '%s': %w", filePath, err)
		}
		return nil
	}

	// Write the selected relative paths, one per line, using standard newline characters.
	content := strings.Join(relativePaths, "\n")
	// Write file with rw-r----- permissions.
	err := os.WriteFile(filePath, []byte(content), 0640)
	if err != nil {
		return fmt.Errorf("failed write persistence file '%s': %w", filePath, err)
	}
	return nil
}

// copyToClipboard attempts to copy the given text to the system clipboard
// using OS-specific commands. Currently supports macOS, Linux (xclip/xsel), Windows.
func copyToClipboard(text string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		// Assume pbcopy is available on macOS.
		cmd = exec.Command("pbcopy")

	case "linux":
		// Prefer xclip if available.
		xclipPath, err := exec.LookPath("xclip")
		if err == nil {
			cmd = exec.Command(xclipPath, "-selection", "clipboard") // Use primary clipboard selection
		} else {
			// Fallback to xsel if xclip is not found.
			xselPath, err := exec.LookPath("xsel")
			if err == nil {
				cmd = exec.Command(xselPath, "--clipboard", "--input") // Arguments for clipboard input
			} else {
				// Neither tool found, provide instructions and return error.
				log.Println("Clipboard error: requires 'xclip' or 'xsel'. Please install one via your package manager (e.g., 'sudo apt install xclip').")
				return fmt.Errorf("clipboard dependency missing: requires 'xclip' or 'xsel'")
			}
		}

	case "windows":
		// Use clip.exe on Windows.
		cmd = exec.Command("clip.exe")

	default:
		// Log content start and return an error for unsupported platforms.
		log.Printf("Clipboard not supported on this OS (%s). Content starts with:\n%.80s...\n", runtime.GOOS, text)
		return fmt.Errorf("clipboard OS unsupported: %s", runtime.GOOS)
	}

	return runClipboardCommand(cmd, text)
}

// runClipboardCommand executes a given clipboard command (like pbcopy, xclip, clip.exe)
// by piping the provided text to its standard input. Reduces code repetition.
func runClipboardCommand(cmd *exec.Cmd, text string) error {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		// Use filepath.Base for a cleaner command name in the error message.
		return fmt.Errorf("failed get stdin pipe for %s: %w", filepath.Base(cmd.Path), err)
	}

	// Write the text to the command's stdin in a separate goroutine.
	// This avoids potential deadlocks if the text buffer is very large.
	go func() {
		// IMPORTANT: Ensure stdin is closed when writing is done or if an error occurs.
		// This signals EOF to the receiving command.
		defer stdin.Close()
		_, err := io.WriteString(stdin, text)
		if err != nil {
			// Log errors from the goroutine, as returning them directly is complex.
			log.Printf("Error writing to %s stdin: %v", filepath.Base(cmd.Path), err)
		}
	}()

	// CombinedOutput captures both stdout and stderr, which is useful for debugging command failures.
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If the command failed (non-zero exit status), log its output and return a wrapped error.
		log.Printf("%s command failed. Output:\n%s", filepath.Base(cmd.Path), string(output))
		return fmt.Errorf("%s command failed: %w", filepath.Base(cmd.Path), err)
	}

	return nil
}

// --- Async Task for Copying ---

// performCopyAndSave is executed as a tea.Cmd (in a separate goroutine by Bubble Tea)
// to handle the potentially time-consuming tasks of reading file metadata and content,
// aggregating it, copying to the clipboard, and saving the final selection state,
// without blocking the main UI thread. It sends a tea.Quit message when finished.
func (m *model) performCopyAndSave(relativePathsToCopy []string) tea.Cmd {
	// Return the function that Bubble Tea will execute asynchronously.
	return func() tea.Msg {
		startTime := time.Now()
		logPrefix := startTime.Format("15:04:05") + " " // Timestamp for log messages generated by this task.
		var contentBuilder bytes.Buffer                 // Use bytes.Buffer for efficient string building.
		readErrors := 0                                 // Count files that couldn't be read.
		statErrors := 0                                 // Count files whose metadata couldn't be retrieved.
		copyErrCount := 0                               // Track if the final clipboard operation failed.

		// --- Read Files and Aggregate Content ---
		for _, relativePath := range relativePathsToCopy {
			// Construct the full, absolute path needed for file system operations.
			fullPath := filepath.Join(m.targetDir, relativePath)

			// --- Get File Metadata (Size, ModTime) ---
			fileInfo, statErr := os.Stat(fullPath)
			if statErr != nil {
				// Log error if metadata cannot be retrieved (e.g., file disappeared, permissions).
				log.Printf(logPrefix+"Stat Err %s: %v", relativePath, statErr)
				statErrors++
				continue
			}
			fileSize := fileInfo.Size()
			modTime := fileInfo.ModTime()

			// --- Read File Content ---
			fileContent, err := os.ReadFile(fullPath)
			if err != nil {
				// Log error if file content cannot be read (e.g., permissions, deleted).
				log.Printf(logPrefix+"Read Err %s: %v", relativePath, err)
				readErrors++
				continue
			}

			// --- Append Header and Content to Buffer ---
			// Create a formatted header including the relative path and metadata.
			header := fmt.Sprintf("--- FILENAME: %s | Modified: %s | Size: %d bytes ---\n",
				relativePath,                          // Use relative path for user clarity.
				modTime.Format("2006-01-02 15:04:05"), // Use a standard, readable format.
				fileSize,
			)
			contentBuilder.WriteString(header)
			contentBuilder.Write(fileContent)
			contentBuilder.WriteString("\n\n") // Add a blank line separator between files.
		}

		// --- Copy Aggregated Content to Clipboard ---
		combinedContent := contentBuilder.String()
		var copyErr error
		// Calculate how many files were successfully processed (had metadata and content read).
		filesSuccessfullyProcessed := len(relativePathsToCopy) - readErrors - statErrors
		// Attempt clipboard copy only if there's actual content gathered.
		if filesSuccessfullyProcessed > 0 {
			copyErr = copyToClipboard(combinedContent)
			if copyErr != nil {
				copyErrCount++
			}
		} else if len(relativePathsToCopy) > 0 {
			// Log if files were selected, but none could be successfully read/processed.
			log.Printf(logPrefix + "Skip clipboard: No content could be read/processed.")
		}

		// --- Save Final Selection State ---
		// Save the list of relative paths that were *intended* for copying (the selection state),
		// regardless of whether reading/copying operations were fully successful.
		saveErr := saveSelections(relativePathsToCopy, m.targetDir)

		// --- Log Final Status Summary ---
		logMsg := "" // Accumulate status message components for the final log line.
		if copyErr != nil {
			logMsg += fmt.Sprintf("Clipboard Error: %v. ", copyErr)
		}
		if saveErr != nil {
			logMsg += fmt.Sprintf("Save Error: %v. ", saveErr)
		}
		if readErrors > 0 {
			logMsg += fmt.Sprintf("%d read err(s). ", readErrors)
		}
		if statErrors > 0 {
			logMsg += fmt.Sprintf("%d stat err(s). ", statErrors)
		}

		// Determine the overall success/failure message based on encountered errors.
		if copyErrCount == 0 && saveErr == nil { // If no critical clipboard or save errors occurred
			if len(relativePathsToCopy) > 0 { // And files were actually selected
				if filesSuccessfullyProcessed > 0 { // And some files were successfully processed
					logMsg = fmt.Sprintf("Copied %d file(s), saved selection.", filesSuccessfullyProcessed)
				} else { // Files were selected, but none could be read/processed
					logMsg = fmt.Sprintf("Saved selection (%d), but no content read/processed.", len(relativePathsToCopy))
				}
			} else { // No files were selected to begin with
				logMsg = "Selection cleared." // Indicates the .yank file was likely removed.
			}
		} else if logMsg == "" { // Errors occurred but weren't formatted into logMsg yet (shouldn't happen)
			logMsg = "Completed with errors."
		}

		// Print the final consolidated log message with the task duration.
		// Uses the standard log package, output appears cleanly after the TUI exits.
		log.Printf(logPrefix+"%s (%.2fs)", logMsg, time.Since(startTime).Seconds())

		// Send the Quit message back to the Bubble Tea runtime to terminate the application.
		return tea.Quit()
	}
}

// --- Helper Function ---

// getPersistenceFilePath constructs the absolute path for the persistence file
func getPersistenceFilePath(targetDir string) string {
	return filepath.Join(targetDir, persistenceDotFileName)
}

// --- Main Function ---

// printHelp displays the command-line usage instructions for the tool.
func printHelp() {
	fmt.Printf("%s: TUI File Copier\n\n", appName)
	fmt.Println(`Recursively scans a directory, allows interactive file selection, and copies the relative path, metadata (modification time, size), and content of selected files to the clipboard.`)
	fmt.Println("\nUsage:")
	fmt.Printf("  %s [-dir <directory>] [-h|-help]\n", appName)
	fmt.Println("\nOptions:")
	flag.PrintDefaults()
	fmt.Println("\nKeybindings (within the TUI):")
	fmt.Println("  --- Normal Mode ---")
	fmt.Println("  j, k, ↓, ↑         Move cursor up/down.")
	fmt.Println("  space, m,          Toggle selection for the focused file/path.")
	fmt.Println("  c, C,              Clear selection.")
	fmt.Println("  .                  Toggle visibility of hidden files/directories (paths containing '.').")
	fmt.Println("                       Selected hidden items remain visible.")
	fmt.Println("  /                  Enter filter mode (fuzzy search).")
	fmt.Println("  y, enter           Confirm selection, copy data to clipboard, save selection, and quit.")
	fmt.Println("  q, ctrl+c          Quit without copying.")
	fmt.Println("  ?                  Show/hide the built-in help view for more keys.")
	fmt.Println("\n  --- Filter Mode ---")
	fmt.Println("  (type)             Enter text to filter list (fuzzy search).")
	fmt.Println("  esc                Exit filter mode and clear filter.")
	fmt.Println("  ctrl+j, ctrl+k     Move cursor down/up within filtered list.")
	fmt.Println("  ctrl+m             Toggle selection for the focused file in filtered list.")
	fmt.Println("  backspace          Delete last character from filter query.")
	fmt.Println("  y, enter           Confirm selection (based on overall checks), copy, save, and quit.")
	fmt.Println("  q, ctrl+c          Quit without copying.")

	fmt.Println("\nFeatures:")
	fmt.Println("  - Recursive Scan: Finds files in all subdirectories (incl. hidden, excluding .git).")
	fmt.Printf("  - Persistence: Remembers the last selection for each directory in a '%s' file.\n", persistenceDotFileName)
	fmt.Println("  - Clipboard Format: Each file's data is preceded by a header:")
	fmt.Println("    --- FILENAME: path/to/file.txt | Modified: YYYY-MM-DD HH:MM:SS | Size: NNN bytes ---")
	fmt.Printf("  - Exclusions: Ignores '.git' directories and the root '%s' state file.\n", persistenceDotFileName)
}

func main() {
	// Configure standard logger - remove default flags (timestamp, file:line) for cleaner output.
	// Logs will appear after the TUI exits.
	log.SetFlags(0)

	// --- Command-Line Flag Parsing ---
	dir := flag.String("dir", ".", "Directory to list files from")
	// Use a separate variable for boolean flags to easily check their value *after* parsing.
	var showHelp bool
	// Define the primary flag (-help) and its shorthand (-h), both modifying the same variable.
	flag.BoolVar(&showHelp, "help", false, "Show help message and exit")
	flag.BoolVar(&showHelp, "h", false, "Show help message and exit (shorthand)")

	flag.Parse()

	// --- Handle Help Flag ---
	// Check if the user requested help via either -h or -help.
	if showHelp {
		printHelp()
		os.Exit(0) // Exit the program cleanly with status 0 (success).
	}

	// --- Process Directory Argument ---
	// Resolve the potentially relative directory path provided by the user (or default ".")
	// to an absolute path for internal consistency.
	targetDir, err := filepath.Abs(*dir)
	if err != nil {
		// Use fmt.Fprintf for errors before TUI starts, as log might not be fully configured.
		fmt.Fprintf(os.Stderr, "Error resolving directory path '%s': %v\n", *dir, err)
		os.Exit(1) // Exit with a non-zero status on critical startup error.
	}
	// Verify the target directory exists and is accessible.
	if _, statErr := os.Stat(targetDir); statErr != nil {
		fmt.Fprintf(os.Stderr, "Target directory '%s' error: %v\n", targetDir, statErr)
		os.Exit(1)
	}

	// --- Start TUI Application ---
	// Create the initial application model, passing the validated target directory.
	m := initialModel(targetDir)

	// Create and run the Bubble Tea program.
	// Using WithAltScreen provides a better user experience by restoring the original
	// terminal screen content when the TUI exits.
	p := tea.NewProgram(m, tea.WithAltScreen())
	// Start the TUI event loop. This call blocks until a tea.Quit message is received
	// (usually triggered by the Quit keybinding or the performCopyAndSave command).
	if _, runErr := p.Run(); runErr != nil {
		// Use log.Fatalf for fatal errors encountered during the TUI lifecycle.
		// log.Fatalf prints the error to stderr and exits the program with status 1.
		log.Fatalf("Error running program: %v\n", runErr)
	}

	// Normal program exit occurs here after tea.Quit message is processed.
	// Any logs from the async performCopyAndSave task will appear in the terminal after this point.
}
