package main

// Utility functions used by the SBOM and Signature commands.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sourcegraph/sourcegraph/lib/errors"
)

const imageListBaseURL = "https://storage.googleapis.com/sourcegraph-release-sboms"
const imageListFilename = "release-image-list.txt"

type cosignConfig struct {
	publicKey                     string
	outputDir                     string
	version                       string
	internalRelease               bool
	insecureIgnoreTransparencyLog bool
	imageFilters                  []string
	excludeImageFilters           []string
}

// TokenResponse represents the JSON response from dockerHub's token service
type dockerHubTokenResponse struct {
	Token string `json:"token"`
}

// getImageDigest returns the sha256 hash for the given image and tag
// It supports multiple registries
func getImageDigest(image string, tag string) (string, error) {
	if strings.HasPrefix(image, "sourcegraph/") {
		return getImageDigestDockerHub(image, tag)
	} else if strings.HasPrefix(image, "us-central1-docker.pkg.dev/") {
		return getImageDigestGcloud(image, tag)
	} else {
		return "", fmt.Errorf("unsupported image registry: %s", image)
	}
}

//
// Implement functionality for Docker Hub

// getImageDigestDockerHub returns the sha256 digest for the given image and tag from DockerHub
func getImageDigestDockerHub(image string, tag string) (string, error) {
	// Construct the DockerHub manifest URL
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/manifests/%s", image, tag)

	token, err := getDockerHubAuthToken(image)
	if err != nil {
		return "", err
	}

	// Create a new HTTP request with the authorization header
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")

	// Make the HTTP request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch image manifest: %v", err)
	}
	defer resp.Body.Close()

	// Check for a successful response
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get manifest - check %s is a valid Sourcegraph release, status code: %d", tag, resp.StatusCode)
	}

	// Get the image digest from the `Docker-Content-Digest` header
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("digest not found in response headers")
	}
	// Return the image's digest (hash)
	return digest, nil
}

// getDockerHubAuthToken returns an auth token with scope to pull the given image
// Note that the token has a short validity so it should be used immediately
func getDockerHubAuthToken(image string) (string, error) {
	// Set the DockerHub authentication URL
	url := fmt.Sprintf("https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull", image)

	// Create a new HTTP request
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to get token: %v", err)
	}
	defer resp.Body.Close()

	// Check if the response status is 200 OK
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get token, status code: %d", resp.StatusCode)
	}

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %v", err)
	}

	// Unmarshal the JSON response
	var tokenResponse dockerHubTokenResponse
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return "", fmt.Errorf("failed to parse token response: %v", err)
	}

	// Return the token
	return tokenResponse.Token, nil
}

//
// Implement functionality for GCP Artifact Registry

// getImageDigestGcloud fetches the OCI image manifest from GCP Artifact Registry and returns the image digest
func getImageDigestGcloud(image string, tag string) (string, error) {
	// Validate image path to ensure it's a valid GCP Artifact Registry image
	if !strings.HasPrefix(image, "us-central1-docker.pkg.dev/") {
		return "", fmt.Errorf("invalid image format: %s", image)
	}

	// Get the GCP access token
	token, err := getGcloudAccessToken()
	if err != nil {
		return "", fmt.Errorf("error getting access token: %v", err)
	}

	parts := strings.SplitN(image, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid image format: %s", image)
	}
	domain := parts[0]
	repositoryPath := parts[1]

	// Create the URL to fetch the manifest for the specific image and tag
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", domain, repositoryPath, tag)

	// Create a new HTTP GET request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %v", err)
	}

	// Add the Authorization and Accept headers
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")

	// Perform the HTTP request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch manifest: %v", err)
	}
	defer resp.Body.Close()

	// Check if the request was successful
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to get manifest, status code: %d, response: %s", resp.StatusCode, string(body))
	}

	// Get the image digest from the `Docker-Content-Digest` header
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("digest not found in response headers")
	}

	return digest, nil
}

// getGcloudAccessToken runs 'gcloud auth print-access-token' and returns the access token
func getGcloudAccessToken() (string, error) {
	// Execute the gcloud command to get the access token
	cmd := exec.Command("gcloud", "auth", "print-access-token")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to retrieve access token using `gcloud auth`. Ensure that gcloud is installed and you have authenticated: %v", err)
	}

	// Trim any extra whitespace or newlines
	token := strings.TrimSpace(string(out))
	return token, nil
}

var spinnerChars = []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}

func spinner(name string, stop chan bool) {
	i := 0
	for {
		select {
		case <-stop:
			return
		default:
			fmt.Printf("\r%s  %s", string(spinnerChars[i%len(spinnerChars)]), name)
			i++
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func getOutputDir(parentDir, version string) string {
	return path.Join(parentDir, "sourcegraph-"+version)
}

// sanitizeVersion removes any leading "v" from the version string
func sanitizeVersion(version string) string {
	return strings.TrimPrefix(version, "v")
}

func verifyCosign() error {
	_, err := exec.LookPath("cosign")
	if err != nil {
		return errors.New("SBOM verification requires 'cosign' to be installed and available in $PATH. See https://docs.sigstore.dev/cosign/system_config/installation/")
	}
	return nil
}

func (c cosignConfig) getImageList() ([]string, error) {
	imageReleaseListURL := c.getImageReleaseListURL()

	resp, err := http.Get(imageReleaseListURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Compare version number against a regex that matches versions up to and including 5.8.0
		versionRegex := regexp.MustCompile(`^v?[0-5]\.([0-7]\.[0-9]+|8\.0)$`)
		if versionRegex.MatchString(c.version) {
			return nil, fmt.Errorf("unsupported version %s: SBOMs are only available for Sourcegraph releases after 5.8.0", c.version)
		}
		return nil, fmt.Errorf("failed to fetch list of images - check that %s is a valid Sourcegraph release: HTTP status %d", c.version, resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var images []string
	for scanner.Scan() {
		image := strings.TrimSpace(scanner.Text())
		if image != "" {
			// Strip off a version suffix if present
			parts := strings.SplitN(image, ":", 2)
			imageName := parts[0]

			// If the --image arg was provided, and if the image name doesn't match any of the filters
			// then skip this image
			if len(c.imageFilters) > 0 && !matchesImageFilter(c.imageFilters, imageName) {
				continue
			}

			// If the --exclude-image arg was provided, and if the image name does match any of the filters
			// then skip this image
			if len(c.excludeImageFilters) > 0 && matchesImageFilter(c.excludeImageFilters, imageName) {
				continue
			}

			images = append(images, imageName)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading image list: %w", err)
	}

	return images, nil
}

// getImageReleaseListURL returns the URL for the list of images in a release, based on the version and whether it's an internal release.
func (c *cosignConfig) getImageReleaseListURL() string {
	if c.internalRelease {
		return fmt.Sprintf("%s/release-internal/%s/%s", imageListBaseURL, c.version, imageListFilename)
	} else {
		return fmt.Sprintf("%s/release/%s/%s", imageListBaseURL, c.version, imageListFilename)
	}
}

// matchesImageFilter checks if the image name from the list of published images
// matches any user-provided --image or --exclude-image glob patterns
// It matches against both the full image name, and the image name without the "sourcegraph/" prefix.
func matchesImageFilter(patterns []string, imageName string) bool {
	for _, pattern := range patterns {
		// Try matching against the full image name
		if matched, _ := filepath.Match(pattern, imageName); matched {
			return true
		}

		// Try matching against the image name without "sourcegraph/" prefix
		if strings.HasPrefix(imageName, "sourcegraph/") {
			shortName := strings.TrimPrefix(imageName, "sourcegraph/")
			if matched, _ := filepath.Match(pattern, shortName); matched {
				return true
			}
		}
	}
	return false
}
