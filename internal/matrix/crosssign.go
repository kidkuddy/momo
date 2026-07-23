package matrix

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
)

// CrossSign makes the bot account sign its own device. Without it every message the
// bot sends shows up in clients as "encrypted by a device not verified by its owner".
//
// With a recovery key it signs against the *existing* cross-signing identity, which
// costs nothing. Without one it generates a new identity, which un-signs every device
// the old identity had signed.
func (c *Client) CrossSign(ctx context.Context, recoveryKey, password string) (newRecoveryKey string, err error) {
	mach := c.ch.Machine()

	if recoveryKey != "" {
		if err := mach.VerifyWithRecoveryKey(ctx, recoveryKey); err != nil {
			return "", fmt.Errorf("sign with recovery key: %w", err)
		}
		return "", nil
	}

	// Replacing an existing identity is gated behind interactive auth. Classic
	// Synapse asks for the password; matrix.org runs MAS, which offers no password
	// stage at all and wants the reset approved in a browser first.
	var challenge *mautrix.RespUserInteractive
	uia := func(resp *mautrix.RespUserInteractive) any {
		challenge = resp
		if password == "" || !hasPasswordStage(resp) {
			return nil
		}
		// mautrix's own ReqUIAuthLogin sends the legacy top-level "user" field,
		// which Synapse rejects, so build the identifier form by hand.
		return map[string]any{
			"type":       mautrix.AuthTypePassword,
			"session":    resp.Session,
			"identifier": map[string]string{"type": "m.id.user", "user": c.mx.UserID.String()},
			"password":   password,
		}
	}
	key, _, err := mach.GenerateAndUploadCrossSigningKeys(ctx, uia, "")
	if err != nil {
		return "", fmt.Errorf("%w%s", err, uiaHint(challenge))
	}
	if err := mach.SignOwnDevice(ctx, mach.OwnIdentity()); err != nil {
		return "", fmt.Errorf("sign own device: %w", err)
	}
	if err := mach.SignOwnMasterKey(ctx); err != nil {
		return "", fmt.Errorf("sign own master key: %w", err)
	}
	return key, nil
}

func hasPasswordStage(resp *mautrix.RespUserInteractive) bool {
	for _, flow := range resp.Flows {
		for _, stage := range flow.Stages {
			if stage == mautrix.AuthTypePassword {
				return true
			}
		}
	}
	return false
}

// uiaHint turns a bare 401 into the approval instruction the server buried in the
// challenge body. Without it the failure looks like a broken password.
func uiaHint(resp *mautrix.RespUserInteractive) string {
	if resp == nil {
		return ""
	}
	for stage, raw := range resp.Params {
		params, _ := raw.(map[string]any)
		if url, ok := params["url"].(string); ok {
			return fmt.Sprintf("\n\nthe server wants this approved first (stage %s):\n  %s\n"+
				"approve it while signed in as the bot account, then run this again", stage, url)
		}
	}
	return ""
}
