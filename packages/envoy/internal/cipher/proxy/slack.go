// packages/envoy/internal/cipher/proxy/slack.go
// The checks a project-channels secret is spent under, a private project's Slack limit. The broker
// marks such a name Limited, with the channels every private project the credential names allows,
// when the credential names a private project, and otherwise gives it Excluded, every channel a
// private project's owner named (requests.AirlockSecret). serveBound runs slackPostAllowed on each
// request a Limited name would carry, and slackCallExcluded on each one a name with Excluded
// channels would carry, after the swap and before anything goes upstream. Both are Go code of this
// package's own, beside the Lean-modelled proxyrules.Decide and proxyrules.Rewrite rather than in
// them, so the model and its fuzz and mutation tests are unchanged.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/requests"
)

const (
	// channelNotAllowed is that refusal's words; the rule the request broke follows them.
	channelNotAllowed = "a private project's grant of this secret posts only with POST /api/chat.postMessage, carrying channel, thread_ts and text, to a Slack channel its project's owner named"
	// channelsSettle ends a CHANNEL_NOT_ALLOWED refusal of a Limited name whose projects' list is
	// settling (requests.AirlockSecret.SettlesAt), before the time it settles at.
	channelsSettle = "; this project's channels changed, and posting to them reopens at "
	// privateChannel is the public fence's refusal's words (PRIVATE_CHANNEL); the rule the call
	// broke follows them.
	privateChannel = "this Slack channel belongs to a private project; only that project's credentials reach it"
	// privateChannelsUnread is the words of PRIVATE_CHANNELS_UNREAD, every call on a name whose
	// public fence the broker could not read (requests.AirlockSecret.ExcludedUnread).
	privateChannelsUnread = "the broker could not read which Slack channels belong to private projects, so this secret serves no Slack call until it can; it asks again within a minute"
	// slackPostPath is the one path a Limited name may be spent on.
	slackPostPath = "/api/chat.postMessage"
	// maxSlackPostBody is the largest body such a post may carry, and the largest body a call the
	// public fence reads may carry, 1 MiB: a message is a few kilobytes, and each check holds the
	// whole body in memory to read it before it goes on.
	maxSlackPostBody = 1 << 20
)

// channelRefusal is a CHANNEL_NOT_ALLOWED refusal's words for secret, which broke the rule why:
// ending, while the list of a private project the credential names is settling, with the time
// posting reopens.
func channelRefusal(secret requests.AirlockSecret, why string) string {
	text := channelNotAllowed + ": " + why
	if !secret.SettlesAt.IsZero() {
		text += channelsSettle + secret.SettlesAt.UTC().Format(time.RFC3339)
	}
	return text
}

// slackPostKeys are the keys such a post may carry, the three slack_post sends
// (packages/envoy-client/src/slack-post.ts). Every other key is refused, metadata first among
// them: Slack's reference says posted metadata "is accessible to any app or user who is a member
// of that workspace", so a post to an allowed channel could otherwise put a private project's
// content where every member reads it. blocks, attachments, reply_broadcast and every unknown
// key go the same way.
var slackPostKeys = map[string]bool{"channel": true, "thread_ts": true, "text": true}

// slackPostAllowed reads r as a post of a Limited name and answers its body when r is one, or why
// it is not. It passes only a request every rule below holds for, and the rules give the body one
// reading, the one a strict receiver makes:
//
//  1. the method is POST, the path is exactly /api/chat.postMessage as sent (no escape in it),
//     and there is no query, since serveBound forwards r.URL's path and query as they came;
//  2. there is exactly one Content-Type, which mime.ParseMediaType reads as application/json or
//     application/x-www-form-urlencoded with at most a charset parameter, utf-8 in any case;
//  3. there is no Content-Encoding, and the body, read once, is at most maxSlackPostBody;
//  4. a JSON body is walked token by token, never decoded into a struct, whose case-insensitive
//     key match would read {"channel":"C_OTHER","CHANNEL":"C_ALLOWED"} as the allowed channel
//     while Slack posts to the other: one object, whose keys, byte for byte, are among
//     slackPostKeys, none twice, channel among them, every value a string, and nothing after it
//     but white space;
//  5. a form body is url.ParseQuery's, keys compared once percent-decoded (chann%65l is channel),
//     each among slackPostKeys with exactly one value, channel among them;
//  6. channel is one of channels byte for byte, so a channel's name, which only Slack resolves,
//     is refused.
//
// The body answered is the bytes read, which the caller serves upstream in place of r's own, so
// the post Slack receives is the post this read.
func slackPostAllowed(r *http.Request, channels []string) (body []byte, why string) {
	switch {
	case r.Method != http.MethodPost:
		return nil, "the method is not POST"
	case r.URL.Path != slackPostPath || r.URL.RawPath != "":
		return nil, "the path is not " + slackPostPath
	case r.URL.RawQuery != "" || r.URL.ForceQuery:
		return nil, "the request carries a query"
	}
	mediaType, why := slackBodyType(r)
	if why != "" {
		return nil, why
	}
	if len(r.Header.Values("Content-Encoding")) != 0 {
		return nil, "the body carries a Content-Encoding"
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSlackPostBody+1))
	switch {
	case err != nil:
		return nil, "the body could not be read"
	case len(body) > maxSlackPostBody:
		return nil, "the body is larger than 1 MiB"
	}
	var channel string
	if mediaType == "application/json" {
		channel, why = jsonPostChannel(body)
	} else {
		channel, why = formPostChannel(body)
	}
	switch {
	case why != "":
		return nil, why
	case !slices.Contains(channels, channel):
		return nil, "the channel is not one its project's owner named"
	}
	return body, ""
}

// jsonPostChannel is the channel of a JSON post as rule 4 reads it, or why the body is not one.
func jsonPostChannel(body []byte) (string, string) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return "", "the JSON body is not one object"
	}
	seen := map[string]bool{}
	var channel string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", "the JSON body cannot be read"
		}
		// An object's key is always a string token; a body that is no JSON fails above.
		key, _ := token.(string)
		switch {
		case !slackPostKeys[key]:
			return "", "the JSON body carries a key other than channel, thread_ts and text"
		case seen[key]:
			return "", "the JSON body carries a key twice"
		}
		seen[key] = true
		token, err = decoder.Token()
		value, isString := token.(string)
		if err != nil || !isString {
			return "", "a value of the JSON body is not a string"
		}
		if key == "channel" {
			channel = value
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return "", "the JSON body cannot be read"
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", "something follows the JSON body's object"
	}
	if !seen["channel"] {
		return "", "the JSON body names no channel"
	}
	return channel, ""
}

// formPostChannel is the channel of a form post as rule 5 reads it, or why the body is not one.
func formPostChannel(body []byte) (string, string) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return "", "the form body cannot be read"
	}
	for key, values := range form {
		switch {
		case !slackPostKeys[key]:
			return "", "the form body carries a key other than channel, thread_ts and text"
		case len(values) != 1:
			return "", "the form body carries a key more than once"
		}
	}
	channel, named := form["channel"]
	if !named {
		return "", "the form body names no channel"
	}
	return channel[0], ""
}

// slackBodyType is the media type of r's body, application/json or
// application/x-www-form-urlencoded, read from its one Content-Type by mime.ParseMediaType with
// at most a charset parameter of utf-8 in any case; or why r's Content-Type is not one of those.
func slackBodyType(r *http.Request) (mediaType, why string) {
	types := r.Header.Values("Content-Type")
	if len(types) != 1 {
		return "", "the request carries no Content-Type, or more than one"
	}
	mediaType, params, err := mime.ParseMediaType(types[0])
	if err != nil {
		return "", "the Content-Type cannot be read"
	}
	for name, value := range params {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return "", "the Content-Type carries a parameter other than charset=utf-8"
		}
	}
	if mediaType != "application/json" && mediaType != "application/x-www-form-urlencoded" {
		return "", "the Content-Type is neither application/json nor application/x-www-form-urlencoded"
	}
	return mediaType, ""
}

// fenceRule is one rule of slackCallExcluded, by the name a test of this package skips it under
// (Options.fenceSkip) to show what it refuses would reach Slack without it.
type fenceRule string

// slackCallExcluded's rules, in the order its comment numbers them.
const (
	// fenceSearch is rule 1: a search method.
	fenceSearch fenceRule = "search"
	// fenceType, fenceEncoding and fenceSize are rule 2: a non-empty body's one Content-Type, its
	// Content-Encoding and its size.
	fenceType     fenceRule = "content-type"
	fenceEncoding fenceRule = "content-encoding"
	fenceSize     fenceRule = "size"
	// fenceObject and fenceStrings are rule 3: a JSON body that is one object and nothing after
	// it, and a channel key's value that is a JSON string.
	fenceObject  fenceRule = "one-object"
	fenceStrings fenceRule = "strings"
	// fenceParse is rule 4: a query or a form url.ParseQuery refuses.
	fenceParse fenceRule = "parse"
	// The rest are rule 5. fenceQuery, fenceJSON and fenceForm are the three places a channel is
	// read from; fenceChannelID and fenceChannels two of the three keys it is read under, and
	// fenceKeyCase a key in any case; fenceEvery every occurrence of a key rather than the first;
	// fenceSplit a value split on commas; fenceFold a part trimmed and upper-cased.
	fenceQuery     fenceRule = "query"
	fenceJSON      fenceRule = "json"
	fenceForm      fenceRule = "form"
	fenceChannelID fenceRule = "channel_id"
	fenceChannels  fenceRule = "channels"
	fenceKeyCase   fenceRule = "key-case"
	fenceEvery     fenceRule = "every"
	fenceSplit     fenceRule = "split"
	fenceFold      fenceRule = "fold"
)

// fenceSkip is the rules a test skips; nil, every production start's, skips none.
type fenceSkip map[fenceRule]bool

// on reports whether rule is checked.
func (s fenceSkip) on(rule fenceRule) bool { return !s[rule] }

// slackCallExcluded reads r as a Slack call on a name the public fence holds and answers its body
// when the call names none of excluded, every channel a private project's owner named, or why it
// may. A credential that names no private project may not read or write such a channel with the
// bot token, so the call is refused when:
//
//  1. it is a search method (search.* or assistant.search.*, a segment of the decoded path,
//     case-folded), which reads every channel the bot is in at once;
//  2. its body, read once, is non-empty and has no single Content-Type that mime.ParseMediaType
//     reads as application/json or application/x-www-form-urlencoded with at most a charset of
//     utf-8, or has a Content-Encoding, or is over maxSlackPostBody;
//  3. a JSON body, walked by tokens, is not one object followed by nothing but white space, or a
//     channel, channel_id or channels key of that object holds, at any of its occurrences, a
//     value that is not a JSON string;
//  4. its query, or a form body, is one url.ParseQuery refuses;
//  5. any value, at every occurrence of channel, channel_id or channels in the query, the JSON
//     object or the form (keys compared once percent-decoded and in any case, so chann%65l and
//     CHANNEL are channel), split on commas, trimmed of white space and upper-cased, is one of
//     excluded upper-cased, as read and once more percent-decoded.
//
// An empty body holds no key. The body answered is the bytes read, which the caller serves
// upstream in place of r's own, so the call Slack receives is the call this read. skip is a
// test's (Options.fenceSkip); nil checks every rule.
func slackCallExcluded(r *http.Request, excluded []string, skip fenceSkip) (body []byte, why string) {
	if skip.on(fenceSearch) && slackSearch(r.URL.Path) {
		return nil, "the call is a Slack search, which reads every channel at once"
	}
	if r.URL.RawQuery != "" && skip.on(fenceQuery) {
		query, err := url.ParseQuery(r.URL.RawQuery)
		switch {
		case err != nil && skip.on(fenceParse):
			return nil, "the query cannot be read"
		case err == nil:
			if why := skip.valuesExcluded(query, excluded); why != "" {
				return nil, "the query " + why
			}
		}
	}
	reader := io.Reader(r.Body)
	if skip.on(fenceSize) {
		reader = io.LimitReader(r.Body, maxSlackPostBody+1)
	}
	body, err := io.ReadAll(reader)
	switch {
	case err != nil:
		return nil, "the body could not be read"
	case len(body) == 0:
		return body, ""
	case len(body) > maxSlackPostBody && skip.on(fenceSize):
		return nil, "the body is larger than 1 MiB"
	case len(r.Header.Values("Content-Encoding")) != 0 && skip.on(fenceEncoding):
		return nil, "the body carries a Content-Encoding"
	}
	mediaType, why := slackBodyType(r)
	switch {
	case why != "" && skip.on(fenceType):
		return nil, why
	case why != "":
		// The negative control of rule 2: a body of no type it reads is read as holding no key.
		return body, ""
	case mediaType == "application/json":
		why = skip.jsonExcluded(body, excluded)
	default:
		why = skip.formExcluded(body, excluded)
	}
	if why != "" {
		return nil, why
	}
	return body, ""
}

// slackSearch reports whether path, as decoded, has a segment naming a Slack search method in any
// case: search.* or assistant.search.*.
func slackSearch(path string) bool {
	for _, segment := range strings.Split(strings.ToLower(path), "/") {
		if strings.HasPrefix(segment, "search.") || strings.HasPrefix(segment, "assistant.search.") {
			return true
		}
	}
	return false
}

// fenceKeys are the keys a Slack call names a channel under.
var fenceKeys = []string{"channel", "channel_id", "channels"}

// channelKey reports whether key, as decoded, is one of fenceKeys, in any case.
func (s fenceSkip) channelKey(key string) bool {
	for _, name := range fenceKeys {
		if (name == "channel_id" && !s.on(fenceChannelID)) || (name == "channels" && !s.on(fenceChannels)) {
			continue
		}
		if key == name || (s.on(fenceKeyCase) && strings.EqualFold(key, name)) {
			return true
		}
	}
	return false
}

// namesExcluded reports whether value names one of excluded: rule 5's reading of one value.
func (s fenceSkip) namesExcluded(value string, excluded []string) bool {
	readings := []string{value}
	if decoded, err := url.QueryUnescape(value); err == nil && decoded != value {
		readings = append(readings, decoded)
	}
	for _, reading := range readings {
		parts := []string{reading}
		if s.on(fenceSplit) {
			parts = strings.Split(reading, ",")
		}
		for _, part := range parts {
			if s.on(fenceFold) {
				part = strings.ToUpper(strings.TrimSpace(part))
			}
			if slices.ContainsFunc(excluded, func(id string) bool { return strings.ToUpper(id) == part }) {
				return true
			}
		}
	}
	return false
}

// valuesExcluded is why a query or a form, as url.ParseQuery read it, names one of excluded, or ""
// when it names none. Its keys are read in order, so the one it names is the same on every read.
func (s fenceSkip) valuesExcluded(values url.Values, excluded []string) string {
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if !s.channelKey(key) {
			continue
		}
		occurrences := values[key]
		if !s.on(fenceEvery) {
			occurrences = occurrences[:1]
		}
		for _, value := range occurrences {
			if s.namesExcluded(value, excluded) {
				return "names a private project's channel under " + key
			}
		}
	}
	return ""
}

// formExcluded is rules 4 and 5 for a form body.
func (s fenceSkip) formExcluded(body []byte, excluded []string) string {
	form, err := url.ParseQuery(string(body))
	switch {
	case err != nil && s.on(fenceParse):
		return "the form body cannot be read"
	case err != nil || !s.on(fenceForm):
		return ""
	}
	if why := s.valuesExcluded(form, excluded); why != "" {
		return "the form body " + why
	}
	return ""
}

// jsonExcluded is rules 3 and 5 for a JSON body, walked token by token: never decoded into a
// struct, whose case-insensitive match keeps one of two keys that differ in case, nor into a map,
// which keeps one occurrence of a key given twice. Every other key's value is skipped whole.
func (s fenceSkip) jsonExcluded(body []byte, excluded []string) string {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		if s.on(fenceObject) {
			return "the JSON body is not one object"
		}
		return ""
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "the JSON body cannot be read"
		}
		// An object's key is always a string token; a body that is no JSON fails above.
		key, _ := token.(string)
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return "the JSON body cannot be read"
		}
		if !s.channelKey(key) || !s.on(fenceJSON) {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			if s.on(fenceStrings) {
				return "the JSON body's " + key + " is not a string"
			}
			continue
		}
		if seen[strings.ToLower(key)] && !s.on(fenceEvery) {
			continue
		}
		seen[strings.ToLower(key)] = true
		if s.namesExcluded(value, excluded) {
			return "the JSON body names a private project's channel under " + key
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return "the JSON body cannot be read"
	}
	if _, err := decoder.Token(); err != io.EOF && s.on(fenceObject) {
		return "something follows the JSON body's object"
	}
	return ""
}
