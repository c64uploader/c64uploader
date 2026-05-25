// APIClient handles communication with C64 Ultimate REST API and FTP server.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
)

// APIClient handles communication with C64 Ultimate REST API.
type APIClient struct {
	Host       string
	HTTPClient *http.Client
}

// APIResponse represents the standard JSON response from C64 Ultimate API.
type APIResponse struct {
	Errors []string `json:"errors"`
}

// NewAPIClient creates a new C64 Ultimate API client.
func NewAPIClient(host string) *APIClient {
	return &APIClient{
		Host: host,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// doRequest performs HTTP request and checks for errors in response.
func (c *APIClient) doRequest(method, path string, body io.Reader) error {
	url := fmt.Sprintf("http://%s%s", c.Host, path)
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	// Parse JSON response.
	var apiResp APIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	// Check for errors in response.
	if len(apiResp.Errors) > 0 {
		return fmt.Errorf("API error: %s", strings.Join(apiResp.Errors, ", "))
	}

	return nil
}

// uploadAndRun uploads a file and executes it using the specified endpoint.
func (c *APIClient) uploadAndRun(endpoint string, fileData []byte) error {
	return c.doRequest("POST", endpoint, bytes.NewReader(fileData))
}

// WriteMemory writes data to C64 memory via DMA.
func (c *APIClient) WriteMemory(address string, data []byte) error {
	path := fmt.Sprintf("/v1/machine:writemem?address=%s", address)
	return c.doRequest("POST", path, bytes.NewReader(data))
}

// ReadMemory reads `length` bytes of C64 memory starting at `address` (hex, no
// prefix) via the machine:readmem DMA endpoint. The device returns raw bytes,
// not JSON, so we do not share the doRequest helper.
func (c *APIClient) ReadMemory(address string, length int) ([]byte, error) {
	url := fmt.Sprintf("http://%s/v1/machine:readmem?address=%s&length=%d", c.Host, address, length)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("readmem returned %s: %s", resp.Status, string(body))
	}
	return io.ReadAll(resp.Body)
}

// MenuButton presses the Ultimate menu button.
func (c *APIClient) MenuButton() error {
	return c.doRequest("PUT", "/v1/machine:menu_button", nil)
}

// Reboot reboots the device firmware.
func (c *APIClient) Reboot() error {
	return c.doRequest("PUT", "/v1/machine:reboot", nil)
}

// resetMachine resets the C64.
func (c *APIClient) resetMachine() error {
	return c.doRequest("PUT", "/v1/machine:reset", nil)
}

// runPRG uploads and runs a .prg file.
func (c *APIClient) runPRG(fileData []byte) error {
	slog.Info("Uploading and running .prg file")
	return c.uploadAndRun("/v1/runners:run_prg", fileData)
}

// runCRT uploads and runs a .crt cartridge file.
func (c *APIClient) runCRT(fileData []byte) error {
	slog.Info("Uploading and running .crt cartridge")
	return c.uploadAndRun("/v1/runners:run_crt", fileData)
}

// runSID uploads and plays a .sid music file.
func (c *APIClient) runSID(fileData []byte) error {
	slog.Info("Uploading and playing .sid file")
	return c.uploadAndRun("/v1/runners:sidplay", fileData)
}

// mountDisk mounts a disk image from the filesystem.
func (c *APIClient) mountDisk(imagePath, imageType string) error {
	path := fmt.Sprintf("/v1/drives/a:mount?image=%s&type=%s&mode=readonly", url.QueryEscape(imagePath), imageType)
	slog.Info("Mounting disk image from filesystem", "path", imagePath, "type", imageType)
	return c.doRequest("PUT", path, nil)
}

// removeDisk removes the mounted disk from drive A.
func (c *APIClient) removeDisk() error {
	slog.Info("Removing previously mounted disk")
	return c.doRequest("PUT", "/v1/drives/a:remove", nil)
}

// ftpConnect establishes a connection to the FTP server and logs in.
func (c *APIClient) ftpConnect(ftpAddr string) (*ftp.ServerConn, error) {
	conn, err := ftp.Dial(ftpAddr, ftp.DialWithTimeout(30*time.Second))
	if err != nil {
		return nil, fmt.Errorf("connecting to FTP server: %w", err)
	}

	// Login (C64 Ultimate typically uses anonymous or no authentication).
	if err := conn.Login("anonymous", "anonymous"); err != nil {
		conn.Quit()
		return nil, fmt.Errorf("FTP login failed: %w", err)
	}

	return conn, nil
}

// ftpUpload uploads file data to the specified destination path via FTP.
func (c *APIClient) ftpUpload(conn *ftp.ServerConn, fileData []byte, destination string) error {
	slog.Info("Uploading file via FTP", "path", destination, "size", len(fileData))

	// Upload file.
	if err := conn.Stor(destination, bytes.NewReader(fileData)); err != nil {
		return fmt.Errorf("FTP upload failed: %w", err)
	}

	slog.Info("FTP upload completed", "path", destination)
	return nil
}

// uploadToTempViaFTP uploads arbitrary file data to the /Temp directory via FTP,
// preserving the caller-supplied filename, and returns the remote path.
func (c *APIClient) uploadToTempViaFTP(fileData []byte, filename string) (string, error) {
	// Connect to FTP server.
	ftpAddr := fmt.Sprintf("%s:21", c.Host)
	conn, err := c.ftpConnect(ftpAddr)
	if err != nil {
		return "", err
	}
	defer conn.Quit()

	// Generate target path in /Temp directory.
	targetPath := filepath.Join("/Temp", filename)

	// Upload file.
	if err := c.ftpUpload(conn, fileData, targetPath); err != nil {
		return "", err
	}

	return targetPath, nil
}

// uploadDiskViaFTP uploads a disk image to the /Temp directory via FTP.
func (c *APIClient) uploadDiskViaFTP(fileData []byte, filename string) (string, error) {
	return c.uploadToTempViaFTP(fileData, filename)
}

// injectKeyboardCommand injects a BASIC command into the C64 keyboard buffer.
func (c *APIClient) injectKeyboardCommand(command string) error {
	// C64 keyboard buffer is at $0277-$02A6 (631-678 decimal).
	// Buffer length counter is at $00C6 (198 decimal).

	// Convert command string to PETSCII bytes.
	petscii := []byte(strings.ToUpper(command))

	// Write command to keyboard buffer.
	if err := c.WriteMemory("0277", petscii); err != nil {
		return fmt.Errorf("writing keyboard buffer: %w", err)
	}

	// Set buffer length.
	bufferLen := []byte{byte(len(petscii))}
	if err := c.WriteMemory("00C6", bufferLen); err != nil {
		return fmt.Errorf("writing buffer length: %w", err)
	}

	return nil
}

// runDiskImage mounts a disk image and runs the first extracted PRG via DMA.
func (c *APIClient) runDiskImage(fileData []byte, imageType, filename string) error {
	// Extract first PRG file from disk image.
	prgData, prgFilename, err := extractFirstPRG(fileData)
	if err != nil {
		return fmt.Errorf("extracting PRG from disk image: %w", err)
	}

	slog.Info("Extracted PRG from disk", "filename", prgFilename, "size", len(prgData), "imageType", imageType)

	// Remove previously mounted disk to free up space.
	if err := c.removeDisk(); err != nil {
		// Log but don't fail - disk might not be mounted.
		slog.Debug("Failed to remove previous disk (may not be mounted)", "error", err)
	}

	// Upload disk image to /Temp via FTP using hardcoded filename to avoid filling /Temp.
	hardcodedFilename := "uploaded_disk." + imageType
	remotePath, err := c.uploadDiskViaFTP(fileData, hardcodedFilename)
	if err != nil {
		return fmt.Errorf("uploading disk via FTP: %w", err)
	}

	// Mount the disk image from filesystem for multi-file support.
	if err := c.mountDisk(remotePath, imageType); err != nil {
		return fmt.Errorf("mounting disk image: %w", err)
	}

	slog.Info("Disk image mounted to drive A")

	// Run the extracted PRG via DMA for fastest startup.
	return c.runPRG(prgData)
}
