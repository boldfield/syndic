package mcp

// This file computes a server's identity: the hash that says where a call goes
// and under whose authority (v9 delta, section 2, "Server identity", and
// section 7, "Generations").
//
// Secrets enter only as reference names and generations. A generation is a
// random identifier kept beside the secret, so neither a value nor a digest of
// one reaches the identity, and refreshing a token leaves it unchanged.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/mcp/jcs"
)

// LoginIdentity is what a login recorded: the issuer, resource and client
// identifier, and the login's generation. It holds no token.
type LoginIdentity struct {
	Issuer     string `json:"issuer"`
	Resource   string `json:"resource"`
	ClientID   string `json:"client_id"`
	Generation string `json:"generation"`
}

// IdentityInputs is what a server's identity needs beyond its table.
type IdentityInputs struct {
	// EnvGenerations maps each env name of the table to the generation of the
	// secret it references.
	EnvGenerations map[string]string
	// AuthGeneration is the generation of the secret named by auth, when the
	// table names one and it is a static credential.
	AuthGeneration string
	// PolicyHash is the hash of the sandbox policy a local server runs under.
	PolicyHash string
	// Login is the recorded login of a remote server using one, else nil.
	Login *LoginIdentity
}

type identityDocument struct {
	Server         config.MCPServer  `json:"server"`
	EnvGenerations map[string]string `json:"env_generations,omitempty"`
	AuthGeneration string            `json:"auth_generation,omitempty"`
	PolicyHash     string            `json:"policy_hash,omitempty"`
	Login          *LoginIdentity    `json:"login,omitempty"`
}

// ServerIdentity returns the lower-case hex SHA-256 of the canonical JSON of
// the server's table as loaded and the inputs. It is computed the same way
// when a call is recorded and when an effect is applied, so the two compare.
func ServerIdentity(srv config.MCPServer, in IdentityInputs) (string, error) {
	doc := identityDocument{
		Server:         srv,
		AuthGeneration: in.AuthGeneration,
		Login:          in.Login,
	}
	names := make([]string, 0, len(srv.Env))
	for n := range srv.Env {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		doc.EnvGenerations = make(map[string]string, len(names))
	}
	for _, n := range names {
		g := in.EnvGenerations[n]
		if g == "" {
			return "", fmt.Errorf("mcp: server identity: no generation for env secret %q", n)
		}
		doc.EnvGenerations[n] = g
	}
	if srv.Transport == config.MCPTransportStdio {
		if in.PolicyHash == "" {
			return "", fmt.Errorf("mcp: server identity: local server has no sandbox policy hash")
		}
		doc.PolicyHash = in.PolicyHash
	}
	if srv.Auth != nil {
		switch {
		case in.Login != nil:
			if in.Login.Generation == "" {
				return "", fmt.Errorf("mcp: server identity: login has no generation")
			}
		case in.AuthGeneration == "":
			return "", fmt.Errorf("mcp: server identity: no generation for auth secret %q", srv.Auth.Name)
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("mcp: server identity: %w", err)
	}
	canon, err := jcs.Canonicalize(raw)
	if err != nil {
		return "", fmt.Errorf("mcp: server identity: %w", err)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}
