package workbench

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ProjectInspection is a read-only snapshot of the open project's shape: its
// working-tree status, a capped file list, inferred languages, recognized
// config files, derived test hints, and a README excerpt. It never contains
// any provider secret.
type ProjectInspection struct {
	Project       ProjectState `json:"project"`
	GitStatus     string       `json:"git_status"`
	Files         []string     `json:"files"`
	Languages     []string     `json:"languages"`
	ReadmeExcerpt string       `json:"readme_excerpt"`
	ConfigFiles   []string     `json:"config_files"`
	TestHints     []string     `json:"test_hints"`
}

// inspectionState holds the most recent project inspection, if any. A nil
// latest value means no inspection has been captured yet.
type inspectionState struct {
	mu     sync.Mutex
	latest *ProjectInspection
}

func newInspectionState() *inspectionState {
	return &inspectionState{}
}

func (s *inspectionState) Get() *ProjectInspection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

func (s *inspectionState) Set(in ProjectInspection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = &in
}

const (
	// maxInspectFiles caps the recursive file listing produced by inspection.
	maxInspectFiles = 200
	// maxReadmeExcerpt caps the README excerpt stored in an inspection.
	maxReadmeExcerpt = 4000
)

// inspectSkipDirs are directory names skipped during the recursive file walk.
var inspectSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".venv":        true,
	"__pycache__":  true,
}

// inspectConfigFiles is the ordered set of recognized project config files.
var inspectConfigFiles = []string{
	"go.mod",
	"package.json",
	"pyproject.toml",
	"requirements.txt",
	"Cargo.toml",
	"Makefile",
	"Taskfile.yml",
	"docker-compose.yml",
	"Dockerfile",
	".gitignore",
}

// extLanguages maps a (lowercased) file extension to a language name.
var extLanguages = map[string]string{
	".go":   "Go",
	".py":   "Python",
	".js":   "JavaScript",
	".ts":   "TypeScript",
	".tsx":  "TypeScript",
	".jsx":  "JavaScript",
	".rs":   "Rust",
	".java": "Java",
	".c":    "C",
	".cpp":  "C++",
	".h":    "C",
	".md":   "Markdown",
	".yaml": "YAML",
	".yml":  "YAML",
	".json": "JSON",
	".toml": "TOML",
	".sh":   "Shell",
}

// errFileCap is the sentinel used to stop the file walk once the cap is hit.
var errFileCap = errors.New("inspect: file cap reached")

// walkProjectFiles returns up to maxInspectFiles paths (relative to root, slash
// separated) found by a recursive walk that skips inspectSkipDirs. The walk is
// best-effort: unreadable entries are skipped rather than failing the listing.
func walkProjectFiles(root string) []string {
	files := make([]string, 0, maxInspectFiles)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if inspectSkipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= maxInspectFiles {
			return errFileCap
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errFileCap) {
		// Best-effort: return whatever was collected before the error.
		return files
	}
	return files
}

// detectLanguages returns the sorted, deduplicated set of languages implied by
// the extensions of the given files.
func detectLanguages(files []string) []string {
	set := map[string]bool{}
	for _, f := range files {
		if lang, ok := extLanguages[strings.ToLower(filepath.Ext(f))]; ok {
			set[lang] = true
		}
	}
	langs := make([]string, 0, len(set))
	for l := range set {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return langs
}

// detectConfigFiles returns the recognized config files present at the project
// root, in inspectConfigFiles order.
func detectConfigFiles(root string) []string {
	found := make([]string, 0, len(inspectConfigFiles))
	for _, name := range inspectConfigFiles {
		if info, err := os.Stat(filepath.Join(root, name)); err == nil && !info.IsDir() {
			found = append(found, name)
		}
	}
	return found
}

// deriveTestHints maps the present config files to test command hints. pytest
// is emitted once even if both Python config files are present.
func deriveTestHints(configFiles []string) []string {
	present := make(map[string]bool, len(configFiles))
	for _, c := range configFiles {
		present[c] = true
	}
	hints := []string{}
	if present["go.mod"] {
		hints = append(hints, "go test ./...")
	}
	if present["package.json"] {
		hints = append(hints, "npm test")
	}
	if present["pyproject.toml"] || present["requirements.txt"] {
		hints = append(hints, "pytest")
	}
	if present["Cargo.toml"] {
		hints = append(hints, "cargo test")
	}
	if present["Makefile"] {
		hints = append(hints, "make test")
	}
	return hints
}

// readReadmeExcerpt returns the first recognized README file's content at root,
// truncated to max runes, or "" if none is present or readable.
func readReadmeExcerpt(root string, max int) string {
	for _, name := range []string{"README.md", "README", "readme.md"} {
		p := filepath.Join(root, name)
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return truncateRunes(string(data), max)
	}
	return ""
}

// inspectProject builds a read-only ProjectInspection. It never mutates the
// project and only shells out to "git -C <path> status --short" for git repos.
func inspectProject(project ProjectState) ProjectInspection {
	insp := ProjectInspection{
		Project:     project,
		Files:       walkProjectFiles(project.Path),
		ConfigFiles: detectConfigFiles(project.Path),
	}
	insp.Languages = detectLanguages(insp.Files)
	insp.TestHints = deriveTestHints(insp.ConfigFiles)
	insp.ReadmeExcerpt = readReadmeExcerpt(project.Path, maxReadmeExcerpt)
	if project.Git {
		if out, err := exec.Command("git", "-C", project.Path, "status", "--short").Output(); err == nil {
			insp.GitStatus = string(out)
		}
	}
	return insp
}

// runInspection inspects the project, stores it as the latest inspection, and
// records a project.inspected event. It returns the produced inspection.
func (wb *Server) runInspection(project ProjectState) ProjectInspection {
	insp := inspectProject(project)
	wb.inspection.Set(insp)
	if data, err := json.Marshal(insp); err == nil {
		wb.store.Append("project.inspected", "Inspected project "+project.Name, data)
	} else {
		wb.store.Append("project.inspected", "Inspected project "+project.Name, nil)
	}
	return insp
}

func (wb *Server) handleProjectInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project := wb.project.Get()
	if project == nil {
		http.Error(w, "no project open", http.StatusConflict)
		return
	}
	insp := wb.runInspection(*project)
	writeJSON(w, http.StatusOK, insp)
}

func (wb *Server) handleProjectInspection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	insp := wb.inspection.Get()
	if insp == nil {
		http.Error(w, "no inspection", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, *insp)
}
