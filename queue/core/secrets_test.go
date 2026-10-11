package core_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/queue/core"
)

var _ = Describe("Redact", func() {
	It("hides only the userinfo inside a URL's authority", func() {
		for in, want := range map[string]string{
			"https://host?contact=dev@example.com":                         "https://host?contact=dev@example.com",
			"https://host/x#dev@example.com":                               "https://host/x#dev@example.com",
			"https://user:pw@host/x":                                       "https://***@host/x",
			"ssh://git@host/x":                                             "ssh://***@host/x",
			"git@host:path":                                                "git@host:path",
			`parse "https://user:pw@host/%zz"`:                             `parse "https://***@host/%zz"`,
			`{"url":"https://host","contact":"dev@example.com"}`:           `{"url":"https://host","contact":"dev@example.com"}`,
			`{"url":"https://user:pw@host/x","contact":"dev@example.com"}`: `{"url":"https://***@host/x","contact":"dev@example.com"}`,
			"https://user:p@ss@host/x":                                     "https://***@host/x",
			"https://user:SEC,RET@host":                                    "https://user:SEC,RET@host", // config refuses it; a registered secret hides it
			"https://us'er:pw@host":                                        "https://us'er:pw@host",
			`https://user:Rv3n'Hq8w"a\b,c@host/%zz`:                        `https://user:Rv3n'Hq8w"a\b,c@host/%zz`,
			"url='https://host',owner='alice@example.com'":                 "url='https://host',owner='alice@example.com'",
			`{"Why":"'https://git.invalid'","Refused":[{"ID":"dev@x"}]}`:   `{"Why":"'https://git.invalid'","Refused":[{"ID":"dev@x"}]}`,
			`{"message":"{\"url\":\"https:\\/\\/user:SECRET@host\"}"}`:     `{"message":"{\"url\":\"https:\\/\\/***@host\"}"}`,
			`{"url":"https:\/\/user:SECRET@host"}`:                         `{"url":"https:\/\/***@host"}`,
			"email me at a@b.com":                                          "email me at a@b.com",
		} {
			Expect(core.Redact(in)).To(Equal(want), in)
		}
	})
})

var _ = Describe("SecretSet", func() {
	It("hides a configured secret in every encoding, then falls back to the URL pattern", func() {
		s := &core.SecretSet{}
		s.Add("SEC&RET")
		s.Add("SEC,RET")
		s.Add("us'er:pw")
		s.Add("abc") // too short to register
		for in, want := range map[string]string{
			`{"url":"https://user:SEC\u0026RET@host"}`:                        `{"url":"https://***@host"}`,
			`{"message":"{\"url\":\"https:\\/\\/user:SEC\\u0026RET@host\"}"}`: `{"message":"{\"url\":\"https:\\/\\/***@host\"}"}`,
			"https://user:SEC,RET@host":                                       "https://***@host",
			"https://us'er:pw@host":                                           "https://***@host",
			"token SEC%26RET and SEC&RET and \"SEC\\u0026RET\"":               "token *** and *** and \"***\"",
			"url='https://host',owner='alice@example.com' abc":                "url='https://host',owner='alice@example.com' abc",
		} {
			Expect(s.Redact(in)).To(Equal(want), in)
		}
	})

	It("registers the raw, query, path, JSON, double JSON, quoted and userinfo forms", func() {
		const secret = `a&b<c>d/e f'g"h`
		s := &core.SecretSet{}
		s.Add(secret)
		j, _ := json.Marshal(secret)
		jj, _ := json.Marshal(string(j[1 : len(j)-1]))
		q := fmt.Sprintf("%q", secret)
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), string(j[1 : len(j)-1]),
			strings.NewReplacer("\\u0026", "&", "\\u003c", "<", "\\u003e", ">").Replace(string(j[1 : len(j)-1])),
			string(jj[1 : len(jj)-1]), q[1 : len(q)-1], strings.ReplaceAll(string(j[1:len(j)-1]), "/", `\/`),
			url.User(secret).String(), url.UserPassword("u", secret).String()[2:]} {
			Expect(s.Redact("<"+form+">")).To(Equal("<***>"), form)
		}
	})
})
