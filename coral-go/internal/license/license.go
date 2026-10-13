// Package license handles Lemon Squeezy license key activation and validation.
package license

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	activateEndpoint   = "https://api.lemonsqueezy.com/v1/licenses/activate"
	validateEndpoint   = "https://api.lemonsqueezy.com/v1/licenses/validate"
	deactivateEndpoint = "https://api.lemonsqueezy.com/v1/licenses/deactivate"
)

const (
	revalidateInterval = 7 * 24 * time.Hour // re-check every 7 days
	offlineGraceDays   = 30                 // work offline for 30 days
)

// setActivateURL overrides the activate endpoint (for testing).
func setActivateURL(url string) { activateEndpoint = url }

// CachedLicense is persisted to ~/.coral/license.json.
type CachedLicense struct {
	LicenseKey    string `json:"license_key"`
	InstanceID    string `json:"instance_id"`
	CustomerName  string `json:"customer_name,omitempty"`
	CustomerEmail string `json:"customer_email,omitempty"`
	ProductName   string `json:"product_name,omitempty"`
	VariantName   string `json:"variant_name,omitempty"`
	ActivatedAt   string `json:"activated_at"`
	LastValidated string `json:"last_validated"`
	Valid         bool   `json:"valid"`
	TrialEndsAt   string `json:"trial_ends_at,omitempty"`
}

// lsResponse is the Lemon Squeezy API response for activate/validate/deactivate.
// The activate endpoint returns "activated", validate returns "valid",
// deactivate returns "deactivated". We map all three so the same struct works
// for all endpoints.
type lsResponse struct {
	Activated   bool   `json:"activated"`
	Deactivated bool   `json:"deactivated"`
	Valid       bool   `json:"valid"`
	Error       string `json:"error,omitempty"`
	LicenseKey  struct {
		Status    string `json:"status"`
		ExpiresAt string `json:"expires_at"`
	} `json:"license_key"`
	Instance struct {
		ID string `json:"id"`
	} `json:"instance"`
	Meta struct {
		CustomerName  string `json:"customer_name"`
		CustomerEmail string `json:"customer_email"`
		ProductName   string `json:"product_name"`
		VariantName   string `json:"variant_name"`
	} `json:"meta"`
}

// Manager handles license checking, activation, and caching.
type Manager struct {
	mu       sync.RWMutex
	cache    *CachedLicense
	filePath string
}

// NewManager creates a license manager that stores state in coralDir.
func NewManager(coralDir string) *Manager {
	m := &Manager{
		filePath: filepath.Join(coralDir, "license.json"),
	}
	m.load()
	return m
}

// VariantName returns the Lemon Squeezy variant name from the cached license (e.g. "Coral Trial Edition").
func (m *Manager) VariantName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cache == nil {
		return ""
	}
	return m.cache.VariantName
}

// IsValid returns whether the app has a valid, non-expired license.
func (m *Manager) IsValid() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cache == nil || !m.cache.Valid {
		return false
	}

	lastValidated, err := time.Parse(time.RFC3339, m.cache.LastValidated)
	if err != nil {
		return false
	}

	// Within revalidation window → valid
	if time.Since(lastValidated) < revalidateInterval {
		return true
	}

	// Past revalidation but within offline grace → still valid (will re-check on next startup)
	if time.Since(lastValidated) < time.Duration(offlineGraceDays)*24*time.Hour {
		return true
	}

	return false
}

// NeedsRevalidation returns true if the license should be re-checked against the API.
func (m *Manager) NeedsRevalidation() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cache == nil || !m.cache.Valid {
		return false
	}

	lastValidated, err := time.Parse(time.RFC3339, m.cache.LastValidated)
	if err != nil {
		return true
	}

	return time.Since(lastValidated) >= revalidateInterval
}

// Revalidate checks the license against Lemon Squeezy and updates the cache.
// Returns true if still valid. Fails gracefully if offline.
func (m *Manager) Revalidate() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cache == nil {
		return false
	}

	resp, err := m.callAPI(validateEndpoint, m.cache.LicenseKey, m.cache.InstanceID)
	if err != nil {
		// Offline — keep current state, don't update last_validated
		return m.cache.Valid
	}

	m.cache.Valid = resp.Valid && resp.LicenseKey.Status == "active"
	if m.cache.Valid {
		m.cache.LastValidated = time.Now().UTC().Format(time.RFC3339)
	}
	if resp.Meta.ProductName != "" {
		m.cache.ProductName = resp.Meta.ProductName
	}
	if resp.Meta.VariantName != "" {
		m.cache.VariantName = resp.Meta.VariantName
	}
	if resp.LicenseKey.ExpiresAt != "" {
		m.cache.TrialEndsAt = resp.LicenseKey.ExpiresAt
	}
	m.save()
	return m.cache.Valid
}

// Activate activates a license key and caches the result.
func (m *Manager) Activate(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	instanceName := machineFingerprint()
	resp, err := m.callAPI(activateEndpoint, key, instanceName)
	if err != nil {
		return fmt.Errorf("failed to reach license server: %w", err)
	}

	if !resp.Activated {
		msg := resp.Error
		if msg == "" {
			msg = "license activation failed"
		}
		return fmt.Errorf("%s", msg)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	m.cache = &CachedLicense{
		LicenseKey:    key,
		InstanceID:    resp.Instance.ID,
		CustomerName:  resp.Meta.CustomerName,
		CustomerEmail: resp.Meta.CustomerEmail,
		ProductName:   resp.Meta.ProductName,
		VariantName:   resp.Meta.VariantName,
		ActivatedAt:   now,
		LastValidated: now,
		Valid:         true,
		TrialEndsAt:   resp.LicenseKey.ExpiresAt,
	}
	return m.save()
}

// Deactivate deactivates the license on Lemon Squeezy and clears local state.
// This frees up the machine slot so the license can be used on another machine.
func (m *Manager) Deactivate() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cache == nil {
		return fmt.Errorf("no active license")
	}

	// Call LS deactivate API — uses instance_id (not instance_name)
	resp, err := m.callDeactivateAPI(m.cache.LicenseKey, m.cache.InstanceID)
	if err != nil {
		return fmt.Errorf("failed to reach license server: %w", err)
	}

	if !resp.Deactivated {
		msg := resp.Error
		if msg == "" {
			msg = "deactivation failed"
		}
		return fmt.Errorf("%s", msg)
	}

	// Clear local state
	m.cache = nil
	os.Remove(m.filePath)
	return nil
}

// Revoke marks the license as invalid locally. Used when a webhook
// notifies us that the license was revoked or subscription cancelled.
func (m *Manager) Revoke() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cache != nil {
		m.cache.Valid = false
		m.save()
	}
}

// GetInfo returns the cached license info (nil if not activated).
func (m *Manager) GetInfo() *CachedLicense {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cache == nil {
		return nil
	}
	c := *m.cache
	return &c
}

func (m *Manager) callAPI(endpoint, licenseKey, instanceID string) (*lsResponse, error) {
	form := url.Values{
		"license_key":   {licenseKey},
		"instance_name": {instanceID},
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(endpoint, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read the full body so we can log it on error
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("[license] API %s returned HTTP %d: %s", endpoint, resp.StatusCode, string(body))
		return nil, fmt.Errorf("license server returned HTTP %d", resp.StatusCode)
	}

	var result lsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[license] API %s returned invalid JSON: %s", endpoint, string(body))
		return nil, fmt.Errorf("invalid API response: %w", err)
	}

	// Log the response for debugging activation issues
	if !result.Valid {
		log.Printf("[license] API %s returned valid=false error=%q status=%q",
			endpoint, result.Error, result.LicenseKey.Status)
	}

	return &result, nil
}

// callDeactivateAPI calls the LS deactivate endpoint with instance_id
// (different from activate/validate which use instance_name).
func (m *Manager) callDeactivateAPI(licenseKey, instanceID string) (*lsResponse, error) {
	form := url.Values{
		"license_key": {licenseKey},
		"instance_id": {instanceID},
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.PostForm(deactivateEndpoint, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("[license] deactivate API returned HTTP %d: %s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("license server returned HTTP %d", resp.StatusCode)
	}

	var result lsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[license] deactivate API returned invalid JSON: %s", string(body))
		return nil, fmt.Errorf("invalid API response: %w", err)
	}
	return &result, nil
}

func (m *Manager) load() {
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return
	}

	// Try encrypted format first (nonce + ciphertext)
	key := deriveEncryptionKey()
	if plaintext, err := decryptLicense(data, key); err == nil {
		var c CachedLicense
		if json.Unmarshal(plaintext, &c) == nil {
			m.cache = &c
			return
		}
	}

	// Encrypted decryption failed — file is tampered, from another machine,
	// or was manually edited. Treat as invalid.
	log.Println("[license] encrypted license file could not be decrypted — re-activation required")
}

func (m *Manager) save() error {
	dir := filepath.Dir(m.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	plaintext, err := json.Marshal(m.cache)
	if err != nil {
		return err
	}
	key := deriveEncryptionKey()
	ciphertext, err := encryptLicense(plaintext, key)
	if err != nil {
		return err
	}
	return os.WriteFile(m.filePath, ciphertext, 0600)
}

// deriveEncryptionKey derives a 256-bit AES key from the machine fingerprint
// using HMAC-SHA256 with a fixed application salt. This ensures the license
// file can only be decrypted on the same machine.
func deriveEncryptionKey() []byte {
	fingerprint := machineFingerprint()
	salt := []byte("coral-license-v1")
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(fingerprint))
	return mac.Sum(nil) // 32 bytes = AES-256
}

// encryptLicense encrypts plaintext with AES-256-GCM.
// Returns: [12-byte nonce][ciphertext+tag]
func encryptLicense(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize()) // 12 bytes
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decryptLicense decrypts AES-256-GCM encrypted data.
// Expects: [12-byte nonce][ciphertext+tag]
func decryptLicense(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// machineFingerprint returns a stable identifier for this machine.
func machineFingerprint() string {
	hostname, _ := os.Hostname()

	// Collect MAC addresses for stability
	var macs []string
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.HardwareAddr != nil && iface.Flags&net.FlagLoopback == 0 {
			macs = append(macs, iface.HardwareAddr.String())
		}
	}
	sort.Strings(macs)

	raw := hostname + "|" + strings.Join(macs, ",")
	hash := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", hash[:8])
}
