// PELFD. Daemon that automatically "installs" .AppBundles by checking their metadata,
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/liamg/tml"
)

const configFilePath = ".config/pelfd.json"

// Options defines the configuration options for the PELFD daemon.
type Options struct {
	DirectoriesToWalk   []string `json:"directories_to_walk"`   // Directories to scan for .AppBundle and .blob files.
	ProbeInterval       int      `json:"probe_interval"`        // Interval in seconds between directory scans.
	IconDir             string   `json:"icon_dir"`              // Directory to store extracted icons.
	AppDir              string   `json:"app_dir"`               // Directory to store .desktop files.
	ProbeExtensions     []string `json:"probe_extensions"`      // File extensions to probe within directories.
	CorrectDesktopFiles bool     `json:"correct_desktop_files"` // Flag to enable automatic correction of .desktop files.
}

// Config represents the overall configuration structure for PELFD, including scanning options and a tracker for installed bundles.
type Config struct {
	Options Options                 `json:"options"` // PELFD configuration options.
	Tracker map[string]*BundleEntry `json:"tracker"` // Tracker mapping bundle paths to their metadata entries.
}

// BundleEntry represents metadata associated with an installed bundle.
type BundleEntry struct {
	Path    string `json:"path"`              // Full path to the bundle file.
	SHA     string `json:"sha"`               // SHA256 hash of the bundle file.
	Png     string `json:"png,omitempty"`     // Path to the PNG icon file, if extracted.
	Xpm     string `json:"xpm,omitempty"`     // Path to the XPM icon file, if extracted.
	Svg     string `json:"svg,omitempty"`     // Path to the SVG icon file, if extracted.
	Desktop string `json:"desktop,omitempty"` // Path to the corrected .desktop file, if processed.
}

func main() {
	log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Starting <green>pelfd</green> daemon"))

	usr, err := user.Current()
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to get current user: <yellow>%v</yellow>", err))
	}

	configPath := filepath.Join(usr.HomeDir, configFilePath)
	config := loadConfig(configPath, usr.HomeDir)

	if err := os.MkdirAll(config.Options.IconDir, 0755); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create icons directory: <yellow>%v</yellow>", err))
	}

	if err := os.MkdirAll(config.Options.AppDir, 0755); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create applications directory: <yellow>%v</yellow>", err))
	}

	probeInterval := time.Duration(config.Options.ProbeInterval) * time.Second

	for {
		processBundle(config, usr.HomeDir)
		time.Sleep(probeInterval)
	}
}

func loadConfig(configPath, homeDir string) Config {
	config := Config{
		Options: Options{
			DirectoriesToWalk:   []string{"~/Programs"},
			ProbeInterval:       90,
			IconDir:             filepath.Join(homeDir, ".local/share/icons"),
			AppDir:              filepath.Join(homeDir, ".local/share/applications"),
			ProbeExtensions:     []string{".AppBundle", ".blob"},
			CorrectDesktopFiles: true,
		},
		Tracker: make(map[string]*BundleEntry),
	}

	file, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Config file does not exist: <green>%s</green>, creating a new one", configPath))
			saveConfig(config, configPath)
			return config
		}
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to open config file <yellow>%s</yellow> %v", configPath, err))
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to decode config file: <yellow>%v</yellow>", err))
	}

	return config
}

func saveConfig(config Config, path string) {
	file, err := os.Create(path)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to save config file: <yellow>%v</yellow>", err))
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to encode config file: <yellow>%v</yellow>", err))
	}
}

func processBundle(config Config, homeDir string) {
	existing := make(map[string]struct{})
	options := config.Options
	entries := config.Tracker
	changed := false

	for _, dir := range options.DirectoriesToWalk {
		dir = strings.Replace(dir, "~", homeDir, 1)
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Scanning directory: <green>%s</green>", dir))

		for _, ext := range options.ProbeExtensions {
			bundles, err := filepath.Glob(filepath.Join(dir, "*"+ext))
			if err != nil {
				log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to scan directory <yellow>%s</yellow> for <yellow>%s</yellow> files: %v", dir, ext, err))
			}

			for _, bundle := range bundles {
				existing[bundle] = struct{}{}

				sha := computeSHA(bundle)
				if entry, checked := entries[bundle]; checked {
					if entry == nil {
						continue
					}

					if entry.SHA != sha {
						if isExecutable(bundle) {
							processBundles(bundle, sha, entries, options.IconDir, options.AppDir, config)
							changed = true
						} else {
							entries[bundle] = nil
						}
					}
				} else {
					if isExecutable(bundle) {
						processBundles(bundle, sha, entries, options.IconDir, options.AppDir, config)
						changed = true
					} else {
						entries[bundle] = nil
					}
				}
			}
		}
	}

	for path := range entries {
		if _, found := existing[path]; !found {
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Bundle no longer exists: <yellow>%s</yellow>", path))
			cleanupBundle(path, entries, options.IconDir, options.AppDir)
			changed = true
		}
	}

	if changed {
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Updating config: <green>%s</green>", filepath.Join(homeDir, configFilePath)))
		saveConfig(config, filepath.Join(homeDir, configFilePath))
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to stat file <yellow>%s</yellow>: <red>%v</red>", path, err))
		return false
	}
	mode := info.Mode()
	return mode&0111 != 0
}

func computeSHA(path string) string {
	file, err := os.Open(path)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to open file <yellow>%s</yellow>: <red>%v</red>", path, err))
		return ""
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to compute SHA256 for file <yellow>%s</yellow>: <red>%v</red>", path, err))
		return ""
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

func processBundles(path, sha string, entries map[string]*BundleEntry, iconPath, appPath string, cfg Config) {
	entry := &BundleEntry{Path: path, SHA: sha}
	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	entry.Png = executeBundle(path, "--pbundle_pngIcon", filepath.Join(iconPath, baseName+".png"))
	entry.Xpm = executeBundle(path, "--pbundle_xpmIcon", filepath.Join(iconPath, baseName+".xpm"))
	entry.Svg = executeBundle(path, "--pbundle_svgIcon", filepath.Join(iconPath, baseName+".svg"))
	entry.Desktop = executeBundle(path, "--pbundle_desktop", filepath.Join(appPath, baseName+".desktop"))

	if entry.Png != "" || entry.Xpm != "" || entry.Desktop != "" {
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Adding bundle to entries: <green>%s</green>", path))
		entries[path] = entry
	} else {
		log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> Bundle does not contain any metadata files. Skipping: <blue>%s</blue>", path))
		entries[path] = nil
	}

	desktopPath := filepath.Join(appPath, baseName+".desktop")

	if _, err := os.Stat(desktopPath); err == nil {
		content, err := os.ReadFile(desktopPath)
		if err != nil {
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to read .desktop file: <yellow>%v</yellow>", err))
			return
		}
		if cfg.Options.CorrectDesktopFiles {
			// Call updateDesktopFile with the determined icon path and bundle entry
			updatedContent, err := updateDesktopFile(string(content), path, entry)
			if err != nil {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to update .desktop file: <yellow>%v</yellow>", err))
				return
			}
			// Remove the existing .desktop file before writing the updated content
			if err := os.Remove(desktopPath); err != nil && !os.IsNotExist(err) {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to remove existing .desktop file: <yellow>%v</yellow>", err))
				return
			}
			// Write the updated content back to the .desktop file
			if err := os.WriteFile(desktopPath, []byte(updatedContent), 0644); err != nil {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to write updated .desktop file: <yellow>%v</yellow>", err))
				return
			}
			/*
				contentAfterUpdate, err := os.ReadFile(desktopPath)
				if err != nil {
					log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to read .desktop file after update: %v", err))
					return
				}
				if string(contentAfterUpdate) != updatedContent {
					log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> The .desktop file was not updated correctly."))
					return
				}
				log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> The .desktop file in the bundle was corrected."))
			*/
		}
	}
}

func executeBundle(bundle, param, outputFile string) string {
	log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Retrieving metadata from <green>%s</green> with parameter: <cyan>%s</cyan>", bundle, param))
	cmd := exec.Command(bundle, param)
	output, err := cmd.Output()
	if err != nil {
		log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> Bundle <blue>%s</blue> with parameter <cyan>%s</cyan> didn't return a metadata file", bundle, param))
		return ""
	}

	outputStr := string(output)
	data, err := base64.StdEncoding.DecodeString(outputStr)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to decode base64 output for <yellow>%s</yellow> <yellow>%s</yellow>: <red>%v</red>", bundle, param, err))
		return ""
	}

	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to write file <yellow>%s</yellow>: <red>%v</red>", outputFile, err))
		return ""
	}

	log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Successfully wrote file: <green>%s</green>", outputFile))
	return outputFile
}

func cleanupBundle(path string, entries map[string]*BundleEntry, iconDir, appDir string) {
	entry := entries[path]
	if entry == nil {
		return
	}

	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	pngPath := filepath.Join(iconDir, baseName+".png")
	xpmPath := filepath.Join(iconDir, baseName+".xpm")
	desktopPath := filepath.Join(appDir, baseName+".desktop")

	filesToRemove := []string{pngPath, xpmPath, desktopPath}
	for _, file := range filesToRemove {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to remove file: <yellow>%s</yellow> <red>%v</red>", file, err))
		} else {
			log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Removed file: <green>%s</green>", file))
		}
	}

	delete(entries, path)
}

func updateDesktopFile(content, bundlePath string, entry *BundleEntry) (string, error) {
	// Update Exec line
	var updatedExec string
	lookPath, err := exec.LookPath(filepath.Base(bundlePath))
	if err != nil {
		// The bundle is not on the system's path, use the full path.
		updatedExec = fmt.Sprintf("Exec=%s", bundlePath)
	} else {
		// The bundle is on the system's path, use just the name of the bundle.
		updatedExec = fmt.Sprintf("Exec=%s", filepath.Base(lookPath))
	}
	// Define a regular expression to match the Exec line.
	reExec := regexp.MustCompile(`(?m)^Exec=.*$`)
	content = reExec.ReplaceAllString(content, updatedExec)
	log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> The bundled .desktop file (<blue>%s</blue>) had an incorrect \"Exec=\" line. <green>It has been corrected</green>", bundlePath))

	// Determine the icon format based on the available icon paths
	var icon string
	if entry.Png != "" {
		icon = filepath.Base(entry.Png)
	} else if entry.Svg != "" {
		icon = filepath.Base(entry.Svg)
	} else if entry.Xpm != "" {
		icon = filepath.Base(entry.Xpm)
	}

	// Update Icon line
	reIcon := regexp.MustCompile(`(?m)^Icon=.*$`)
	if icon != "" {
		newIconLine := fmt.Sprintf("Icon=%s", icon)
		content = reIcon.ReplaceAllString(content, newIconLine)
	}
	log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> The bundled .desktop file (<blue>%s</blue>) had an incorrect \"Icon=\" line. <green>It has been corrected</green>", bundlePath))

	// Add TryExec line
	reTryExec := regexp.MustCompile(`(?m)^TryExec=.*$`)
	newTryExecLine := fmt.Sprintf("TryExec=%s", filepath.Base(bundlePath))
	content = reTryExec.ReplaceAllString(content, newTryExecLine)
	log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> The bundled .desktop file (<blue>%s</blue>) had an incorrect \"TryExec=\" line. <green>It has been corrected</green>", bundlePath))

	return content, nil
}
