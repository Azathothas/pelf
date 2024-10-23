package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/u-root/u-root/pkg/ldd"
)

// Constants for directory structure
const (
	defaultDstDir    = "output"
	defaultSharedDir = "shared"
	defaultLibDir    = "lib"
	defaultBinDir    = "bin"
)

var (
	strip       = flag.Bool("strip", false, "Strip debug symbols")
	oneDir      = flag.Bool("one-dir", true, "Use one directory for output")
	createLinks = flag.Bool("create-links", true, "Create symlinks in the bin directory")
	dstDirPath  = flag.String("dst-dir", defaultDstDir, "Destination directory for libraries and binaries")
)

// tryStrip attempts to strip the binary if the flag is set
func tryStrip(filePath string) error {
	if *strip {
		stripPath, err := exec.LookPath("strip")
		if err != nil {
			return fmt.Errorf("strip command not found: %v", err)
		}

		// Execute the strip command
		cmd := exec.Command(stripPath, filePath)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to strip %s: %v", filePath, err)
		}
	}
	return nil
}

func isDynamicExecutable(binaryPath string) (bool, error) {
	cmd := exec.Command("ldd", binaryPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, nil
	}
	outputStr := strings.TrimSpace(string(output))

	// Check if the binary is static
	outputLower := strings.ToLower(outputStr)
	if strings.Contains(outputLower, "not a dynamic executable") || strings.Contains(outputLower, "not a valid dynamic program") {
		return false, nil // It's static
	}
	return true, nil
}

// copyFile copies a file from source to destination
func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, input, 0644)
}

// createSymlink creates a symlink at dst pointing to src
func createSymlink(src, dst string) error {
	return os.Symlink(src, dst)
}

// findDynExec finds the dynexec executable in the user's $PATH
func findDynExec() (string, error) {
	path, err := exec.LookPath("sharun")
	if err != nil {
		return "", fmt.Errorf("sharun not found in PATH: %v", err)
	}
	return path, nil
}

func processBinary(binaryPath string) error {
	fileInfo, err := os.Stat(binaryPath)
	if err != nil {
		return err
	}

	if !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("skipped: %s is not a regular file", binaryPath)
	}

	// Check if the binary is dynamic
	isDynamic, err := isDynamicExecutable(binaryPath)
	if err != nil {
		return err
	}

	// Create the main destination directory
	if err := os.MkdirAll(*dstDirPath, 0755); err != nil {
		return err
	}

	if !isDynamic {
		// Handle static binaries
		binDir := filepath.Join(*dstDirPath, defaultBinDir)
		if err := os.MkdirAll(binDir, 0755); err != nil {
			return err
		}
		dstBinaryPath := filepath.Join(binDir, fileInfo.Name())
		if err := copyFile(binaryPath, dstBinaryPath); err != nil {
			return err
		}
		if err := tryStrip(dstBinaryPath); err != nil {
			return err
		}
		if err := os.Chmod(dstBinaryPath, 0755); err != nil {
			return err
		}
		fmt.Printf("Processed static binary: %s\n", fileInfo.Name())
		return nil
	}

	// Process dynamic binaries
	dynexecPath, err := findDynExec()
	if err != nil {
		return err
	}

	dstDynExec := filepath.Join(*dstDirPath, "sharun")
	if err := copyFile(dynexecPath, dstDynExec); err != nil {
		return err
	}
	if err := os.Chmod(dstDynExec, 0755); err != nil {
		return err
	}

	binDir := filepath.Join(*dstDirPath, defaultBinDir)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return err
	}

	symlinkPath := filepath.Join(binDir, fileInfo.Name())
	if *createLinks {
		if err := createSymlink("../sharun", symlinkPath); err != nil {
			return err
		}
	}

	sharedDir := filepath.Join(*dstDirPath, defaultSharedDir)
	sharedBinDir := filepath.Join(sharedDir, defaultBinDir)
	sharedLibDir := filepath.Join(sharedDir, defaultLibDir)

	if err := os.MkdirAll(sharedBinDir, 0755); err != nil {
		return err
	}

	if err := os.MkdirAll(sharedLibDir, 0755); err != nil {
		return err
	}

	sharedBinaryPath := filepath.Join(sharedBinDir, fileInfo.Name())
	if err := copyFile(binaryPath, sharedBinaryPath); err != nil {
		return err
	}
	if err := tryStrip(sharedBinaryPath); err != nil {
		return err
	}

	// Get the list of libraries, including the dynamic linker
	libPaths, err := getLibs(binaryPath)
	if err != nil {
		return err
	}

	// Copy libraries to the shared lib directory
	for _, libPath := range libPaths {
		dstLibPath := filepath.Join(sharedLibDir, filepath.Base(libPath))
		if err := copyFile(libPath, dstLibPath); err != nil {
			return err
		}
		if err := tryStrip(dstLibPath); err != nil {
			return err
		}
	}
	fmt.Printf("Processed dynamic binary: %s\n", fileInfo.Name())
	return nil
}

// getLibs retrieves the list of libraries that a binary depends on
func getLibs(binaryPath string) ([]string, error) {
	dependencies, err := ldd.FList(binaryPath)
	if err != nil {
		return nil, err
	}
	return dependencies, nil
}

func main() {
	flag.Parse()

	binaryList := flag.Args()
	if len(binaryList) == 0 {
		fmt.Println("Error: Specify the ELF binary executable!")
		os.Exit(1)
	}

	if *oneDir && *dstDirPath == "" {
		*dstDirPath = defaultDstDir
	}

	for _, binary := range binaryList {
		if err := processBinary(binary); err != nil {
			log.Printf("Error processing %s: %v\n", binary, err)
		}
	}
}
