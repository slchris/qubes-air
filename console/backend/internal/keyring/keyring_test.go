package keyring

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	key1 = "0123456789abcdef0123456789abcdef" // 32 bytes
	key2 = "fedcba9876543210fedcba9876543210" // 32 bytes
)

func TestNewSingle(t *testing.T) {
	kr, err := NewSingle([]byte(key1))
	require.NoError(t, err)
	assert.Equal(t, 1, kr.PrimaryVersion())
	assert.Equal(t, []int{1}, kr.Versions())

	k, err := kr.PrimaryKey()
	require.NoError(t, err)
	assert.Equal(t, []byte(key1), k)
}

func TestNew_RejectsBadKeyLength(t *testing.T) {
	_, err := New(map[int][]byte{1: []byte("too-short")}, 1)
	assert.Error(t, err)
}

func TestNew_RejectsMissingPrimary(t *testing.T) {
	_, err := New(map[int][]byte{1: []byte(key1)}, 2)
	assert.Error(t, err)
}

func TestNew_RejectsZeroVersion(t *testing.T) {
	_, err := New(map[int][]byte{0: []byte(key1)}, 0)
	assert.Error(t, err)
}

func TestKey_MissingVersionErrors(t *testing.T) {
	kr, err := NewSingle([]byte(key1))
	require.NoError(t, err)
	_, err = kr.Key(2)
	assert.Error(t, err)
}

func TestParseSpec_MultiVersion(t *testing.T) {
	kr, err := ParseSpec("v1:" + key1 + ",v2:" + key2)
	require.NoError(t, err)

	assert.Equal(t, 2, kr.PrimaryVersion(), "highest version is primary")
	assert.Equal(t, []int{1, 2}, kr.Versions())

	k1, err := kr.Key(1)
	require.NoError(t, err)
	assert.Equal(t, []byte(key1), k1)

	k2, err := kr.Key(2)
	require.NoError(t, err)
	assert.Equal(t, []byte(key2), k2)
}

func TestParseSpec_WithoutVPrefix(t *testing.T) {
	kr, err := ParseSpec("1:" + key1 + ", 2:" + key2)
	require.NoError(t, err)
	assert.Equal(t, 2, kr.PrimaryVersion())
}

func TestParseSpec_OrderIndependent(t *testing.T) {
	// Highest version wins as primary regardless of listing order.
	kr, err := ParseSpec("v2:" + key2 + ",v1:" + key1)
	require.NoError(t, err)
	assert.Equal(t, 2, kr.PrimaryVersion())
}

func TestParseSpec_Errors(t *testing.T) {
	tests := []struct {
		name string
		spec string
	}{
		{"empty", ""},
		{"no colon", "v1" + key1},
		{"bad version", "vX:" + key1},
		{"short key", "v1:short"},
		{"duplicate version", "v1:" + key1 + ",v1:" + key2},
		{"zero version", "v0:" + key1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSpec(tt.spec)
			assert.Error(t, err)
		})
	}
}
