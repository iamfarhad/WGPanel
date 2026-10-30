package wgkeys

import (
	"fmt"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// GeneratePresharedKey returns a fresh 32-byte WireGuard PresharedKey (base64). Like a
// private key it must be encrypted (Encrypt) before storage.
func GeneratePresharedKey() (string, error) {
	k, err := wgtypes.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("generate preshared key: %w", err)
	}
	return k.String(), nil
}
