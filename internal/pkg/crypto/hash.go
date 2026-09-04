package crypto

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/sync/semaphore"
)

type Argon2Params struct {
	Time       uint32
	Memory     uint32 // KiB
	Threads    uint8
	KeyLength  uint32
	SaltLength uint32
}

var DefaultArgon2Params = Argon2Params{
	Time:       3,
	Memory:     64 * 1024, // 64 MB
	Threads:    4,
	KeyLength:  32,
	SaltLength: 16,
}

// 64 MB × 4 threads per verify is heavy. Cap concurrent Argon2 operations
// to avoid exhausting memory under a login burst.
var argon2Sem = semaphore.NewWeighted(int64(min(runtime.NumCPU(), 8)))

// HashPassword hashes a password using Argon2id with a random salt.
// Returns a PHC-format string: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func HashPassword(ctx context.Context, password string, params Argon2Params) (string, error) {
	if err := argon2Sem.Acquire(ctx, 1); err != nil {
		return "", fmt.Errorf("argon2 semaphore: %w", err)
	}
	defer argon2Sem.Release(1)

	salt := make([]byte, params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, params.Time, params.Memory, params.Threads, params.KeyLength)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		params.Memory, params.Time, params.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)

	return encoded, nil
}

// VerifyPassword verifies a password against a PHC-format Argon2id hash.
func VerifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	params, salt, hash, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}

	if err := argon2Sem.Acquire(ctx, 1); err != nil {
		return false, fmt.Errorf("argon2 semaphore: %w", err)
	}
	defer argon2Sem.Release(1)

	otherHash := argon2.IDKey([]byte(password), salt, params.Time, params.Memory, params.Threads, params.KeyLength)

	return subtle.ConstantTimeCompare(hash, otherHash) == 1, nil
}

func decodeHash(encoded string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return Argon2Params{}, nil, nil, fmt.Errorf("invalid hash format")
	}

	var params Argon2Params
	_, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.Memory, &params.Time, &params.Threads)
	if err != nil {
		return Argon2Params{}, nil, nil, fmt.Errorf("parse params: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, fmt.Errorf("decode salt: %w", err)
	}
	params.SaltLength = uint32(len(salt))

	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Argon2Params{}, nil, nil, fmt.Errorf("decode hash: %w", err)
	}
	params.KeyLength = uint32(len(hash))

	return params, salt, hash, nil
}