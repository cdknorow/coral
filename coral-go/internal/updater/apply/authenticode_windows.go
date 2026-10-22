//go:build windows

package apply

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modWintrust                        = windows.NewLazySystemDLL("wintrust.dll")
	procWTHelperProvDataFromStateData  = modWintrust.NewProc("WTHelperProvDataFromStateData")
	procWTHelperGetProvSignerFromChain = modWintrust.NewProc("WTHelperGetProvSignerFromChain")
)

// cryptProviderSgnr is the start of CRYPT_PROVIDER_SGNR (wintrust.h), up to the signer's chain context.
type cryptProviderSgnr struct {
	Size             uint32
	VerifyAsOf       windows.Filetime
	CertChainCount   uint32
	CertChain        uintptr
	SignerType       uint32
	Signer           uintptr
	Error            uint32
	CounterSignerCnt uint32
	CounterSigners   uintptr
	ChainContext     *windows.CertChainContext
}

const (
	trustENoSignature        = 0x800B0100 // TRUST_E_NOSIGNATURE
	trustESubjectFormUnknown = 0x800B0003 // TRUST_E_SUBJECT_FORM_UNKNOWN
)

// verifyAuthenticode checks the held file's Authenticode signature through the held handle (so the bytes checked are the bytes msiexec
// gets): WinVerifyTrust with the generic verify policy and revocation checked on the whole chain, then the pin (authenticode_pin.go). An
// unsigned file is (false, nil); anything else that doesn't pass is an error.
func verifyAuthenticode(h *heldFile) (bool, error) {
	path, err := windows.UTF16PtrFromString(h.path)
	if err != nil {
		return false, err
	}
	file := &windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: path, File: windows.Handle(h.f.Fd())}
	data := &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_WHOLECHAIN,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(file),
		ProvFlags:                       windows.WTD_REVOCATION_CHECK_CHAIN,
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	defer func() {
		data.StateAction = windows.WTD_STATEACTION_CLOSE
		_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	}()
	if verifyErr != nil {
		var errno windows.Errno
		if errors.As(verifyErr, &errno) && (uint32(errno) == trustENoSignature || uint32(errno) == trustESubjectFormUnknown) {
			return false, nil
		}
		return true, fmt.Errorf("the package's signature doesn't verify: %w", verifyErr)
	}
	provData, _, _ := procWTHelperProvDataFromStateData.Call(uintptr(data.StateData))
	if provData == 0 {
		return true, errors.New("no signer data")
	}
	sgnrPtr, _, _ := procWTHelperGetProvSignerFromChain.Call(provData, 0, 0, 0)
	if sgnrPtr == 0 {
		return true, errors.New("no signer")
	}
	sgnr := *(**cryptProviderSgnr)(unsafe.Pointer(&sgnrPtr)) // the pointer the call returned, as the struct it points to
	chain := sgnr.ChainContext
	if chain == nil || chain.ChainCount == 0 {
		return true, errors.New("no certificate chain")
	}
	simple := *chain.Chains
	elems := unsafe.Slice(simple.Elements, simple.NumElements)
	if len(elems) == 0 {
		return true, errors.New("an empty certificate chain")
	}
	var sums []string
	for _, e := range elems {
		c := e.CertContext
		der := unsafe.Slice(c.EncodedCert, c.Length)
		s := sha256.Sum256(der)
		sums = append(sums, hex.EncodeToString(s[:]))
	}
	leaf := elems[0].CertContext
	return true, checkSignerPin(certAttr(leaf, "2.5.4.10"), certAttr(leaf, "2.5.4.3"), sums)
}

// certAttr is the certificate subject's attribute with the OID (2.5.4.10 O, 2.5.4.3 CN).
func certAttr(c *windows.CertContext, oid string) string {
	o, _ := windows.BytePtrFromString(oid)
	buf := make([]uint16, 512)
	n := windows.CertGetNameString(c, windows.CERT_NAME_ATTR_TYPE, 0, unsafe.Pointer(o), &buf[0], uint32(len(buf)))
	if n <= 1 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}
