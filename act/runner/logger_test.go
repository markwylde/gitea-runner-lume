// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"encoding/base64"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValueMasker(t *testing.T) {
	table := []struct {
		name       string
		lines      string
		secrets    map[string]string
		masks      []string
		disallowed []string
	}{
		{
			name:  "Multiline Private Key",
			lines: "cat << EOF > private.key\nPRIVATE_KEY_BEGIN\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\nPRIVATE_KEY_END\nEOF",
			secrets: map[string]string{
				"PRIVATE_KEY": "PRIVATE_KEY_BEGIN\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\nPRIVATE_KEY_END",
			},
			disallowed: []string{"KEY", "dsdfseffefsefes", "PRIVATE_KEY_END"},
		},
		{
			name:       "Multiline Private Key in masks",
			lines:      "cat << EOF > private.key\nPRIVATE_KEY_BEGIN\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\nPRIVATE_KEY_END\nEOF",
			masks:      []string{"PRIVATE_KEY_BEGIN\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\ndsdfseffefsefes\nPRIVATE_KEY_END"},
			disallowed: []string{"KEY", "dsdfseffefsefes", "PRIVATE_KEY_END"},
		},
		{
			name:       "Secret containing a percent sign",
			lines:      "##[error]login failed for pass%25word",
			secrets:    map[string]string{"TOKEN": "pass%word"},
			disallowed: []string{"pass%25word"},
		},
	}
	for _, entry := range table {
		t.Run(entry.name, func(t *testing.T) {
			ctx := WithMasks(t.Context(), &entry.masks)
			masker := valueMasker(false, entry.secrets)
			for line := range strings.SplitSeq(entry.lines, "\n") {
				lentry := masker(&logrus.Entry{
					Context: ctx,
					Message: line,
				})
				for _, line := range entry.disallowed {
					assert.NotContains(t, lentry.Message, line)
				}
			}
		})
	}
}

// A secret that reaches the log through an encoding — a base64 payload, a JSON body, a
// URL — must be masked as well: masking only the verbatim value leaks it.
func TestValueMaskerEncodedSecrets(t *testing.T) {
	secret := `p@ss w"rd/1`
	masker := valueMasker(false, map[string]string{"TOKEN": secret})

	for _, tc := range []struct {
		name string
		line string
	}{
		{"verbatim", "the token is " + secret},
		{"base64", "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(secret))},
		{"json", `{"token":"` + jsonStringEscape(secret) + `"}`},
		{"query escaped", "https://example.com/?token=" + url.QueryEscape(secret)},
		{"path escaped", "https://example.com/" + url.PathEscape(secret) + "/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := masker(&logrus.Entry{Context: t.Context(), Message: tc.line})

			assert.Contains(t, entry.Message, "***")
			assert.NotContains(t, entry.Message, secret)
			assert.NotContains(t, entry.Message, base64.StdEncoding.EncodeToString([]byte(secret)))
			assert.NotContains(t, entry.Message, url.QueryEscape(secret))
		})
	}
}

// A secret containing " together with <, > or & serializes to JSON differently depending
// on the runtime: act's own toJSON (and Go) HTML-escape <>&, while a JavaScript
// (JSON.stringify) or .NET action leaves them literal. The secret must be masked in either
// form, so a JS-serialized JSON body does not leak it.
func TestValueMaskerJSONEscapesBothWays(t *testing.T) {
	secret := `a"<b>&c`
	masker := valueMasker(false, map[string]string{"TOKEN": secret})

	for _, tc := range []struct {
		name string
		form string
	}{
		{"html escaped (act toJSON / Go)", jsonStringEscape(secret)},
		{"literal (JS JSON.stringify / .NET)", jsonStringEscapeNoHTML(secret)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := masker(&logrus.Entry{Context: t.Context(), Message: `{"t":"` + tc.form + `"}`})

			assert.Contains(t, entry.Message, "***")
			assert.NotContains(t, entry.Message, tc.form)
		})
	}
}

// ::add-mask:: values go through the same masker, so they get the same treatment.
func TestValueMaskerEncodedMasks(t *testing.T) {
	masks := []string{"s3cr3t value"}
	masker := valueMasker(false, nil)

	entry := masker(&logrus.Entry{
		Context: WithMasks(t.Context(), &masks),
		Message: "encoded: " + base64.StdEncoding.EncodeToString([]byte("s3cr3t value")),
	})

	assert.Equal(t, "encoded: ***", entry.Message)
}

// A token in a Basic auth header is base64'd together with the user name, so the token's
// own base64 only appears when the prefix length is a multiple of three. The other two
// alignments must be masked as well, or `Authorization: Basic base64("user:token")` leaks
// the token to anyone who can decode the log.
func TestValueMaskerBase64Alignments(t *testing.T) {
	secret := "s3cr3t-token-value"
	masker := valueMasker(false, map[string]string{"TOKEN": secret})

	// One prefix per alignment: len%3 of 0, 1 and 2.
	for _, prefix := range []string{"x-access-token:", "user:", "ab:"} {
		t.Run(prefix, func(t *testing.T) {
			encoded := base64.StdEncoding.EncodeToString([]byte(prefix + secret))
			entry := masker(&logrus.Entry{Context: t.Context(), Message: "Authorization: Basic " + encoded})

			assert.Contains(t, entry.Message, "***")
			// The aligned middle of the secret must be gone, so the payload can no longer be
			// decoded back into the token.
			assert.NotEqual(t, "Authorization: Basic "+encoded, entry.Message)
			decodable := strings.TrimPrefix(entry.Message, "Authorization: Basic ")
			decoded, err := base64.StdEncoding.DecodeString(decodable)
			if err == nil {
				assert.NotContains(t, string(decoded), secret)
			}
		})
	}
}

// The masker caches its replacer, so it has to notice both a mask appended to the same
// slice and a composite action logging with a slice of its own.
func TestValueMaskerCachedReplacerSeesNewMasks(t *testing.T) {
	masker := valueMasker(false, map[string]string{"TOKEN": "secret-token"})
	mask := func(masks *[]string, message string) string {
		return masker(&logrus.Entry{Context: WithMasks(t.Context(), masks), Message: message}).Message
	}

	job := []string{"first mask"}
	assert.Equal(t, "a *** and ***", mask(&job, "a first mask and secret-token"))

	// ::add-mask:: appends to the same slice
	job = append(job, "second mask")
	assert.Equal(t, "*** and ***", mask(&job, "first mask and second mask"))

	// a composite action brings its own slice
	composite := []string{"composite mask"}
	assert.Equal(t, "*** but first mask", mask(&composite, "composite mask but first mask"))

	// and the job's masks still apply once it is back
	assert.Equal(t, "*** and *** but composite mask", mask(&job, "first mask and second mask but composite mask"))
}

func TestAppendSecretMaskerSkipsUselessEncodings(t *testing.T) {
	// A token with no character an escape would touch only gains its base64 forms:
	// JSON, query and path escaping all leave it unchanged.
	pairs := AppendSecretMasker(nil, "plaintoken")
	assert.Equal(t, []string{
		"plaintoken", "***",
		base64.StdEncoding.EncodeToString([]byte("plaintoken")), "***",
		// The two shifted alignments, each without its leading and trailing group.
		"YWludG9r", "***",
		"bGFpbnRv", "***",
	}, pairs)

	// Too short to mask.
	assert.Empty(t, AppendSecretMasker(nil, "x"))
}

func TestJobLogFormatterDecodesCommandData(t *testing.T) {
	logger := logrus.New()
	logger.Out = io.Discard
	format := func(message string) string {
		out, err := (&jobLogFormatter{}).Format(&logrus.Entry{Logger: logger, Message: message, Data: logrus.Fields{rawOutputField: true}})
		require.NoError(t, err)
		return string(out)
	}

	assert.Contains(t, format("##[error]deploy 50%25 traffic"), "##[error]deploy 50% traffic")
	// a plain line is not command data and keeps its literal escapes
	assert.Contains(t, format("progress 50%25 done"), "progress 50%25 done")
}
