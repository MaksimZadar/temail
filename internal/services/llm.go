package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/llm"
	"github.com/MaksimZadar/temail/internal/secrets"
)

// LLMKeyStore holds provider API keys. *secrets.KeyringStore satisfies it.
// GetLLMKey reads the effective key (environment first, then keyring).
// StoredLLMKey reads only the keyring, which rollback needs. Keys are read here
// and passed to the llm client. They are never returned to the frontend.
type LLMKeyStore interface {
	GetLLMKey(provider string) (string, error)
	StoredLLMKey(provider string) (string, error)
	SetLLMKey(provider, key string) error
	DeleteLLMKey(provider string) error
}

// Limits and timeouts for the LLM bound methods.
const (
	maxProviderName  = 64
	maxProviderModel = 200
	maxAPIKeyLen     = 4096
	llmCallTimeout   = 30 * time.Second
)

// providerNameRE limits names to what a keyring user key and an environment
// variable name can hold without surprises.
var providerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Errors the LLM methods return. Messages are safe to show.
var (
	ErrProviderNotFound = errors.New("no LLM provider with that name")
	// ErrKeyNeedsReentry means a key exists (keyring or environment) that was not
	// approved for this base URL. The key is not used, so the user must enter it
	// again for this address.
	ErrKeyNeedsReentry = errors.New("an API key exists that was not approved for this base URL; enter the API key again")
	// ErrEnableDeclined means enabling a cloud provider was not confirmed in the
	// native dialog (or no dialog was available). Nothing was saved.
	ErrEnableDeclined = errors.New("enabling a cloud LLM provider was not confirmed; nothing was saved")
	// ErrProviderChanged means the provider changed while the enable dialog was
	// open. Nothing was saved; try again.
	ErrProviderChanged = errors.New("the provider changed while it was being confirmed; try again")
)

// LLMService manages LLM providers for the UI. It stores settings in
// config.json and keys in the keyring, and it calls a provider only for the
// explicit bound methods. No mail content flows through it.
type LLMService struct {
	cfg     ConfigStore
	keys    LLMKeyStore
	confirm CloudConfirmer // nil means "no dialog": enabling cloud is refused

	// cfgMu serialises config and key writes. A writer holds it across its
	// keyring write and its config save, and a reader takes a snapshot of the
	// config and the key under it. Network calls never run under it.
	cfgMu sync.Mutex
}

// NewLLMService returns an LLMService. confirm asks the user before a cloud
// provider is enabled; pass nil only where no dialog exists (tests, headless),
// and then enabling cloud is refused.
func NewLLMService(cfg ConfigStore, keys LLMKeyStore, confirm CloudConfirmer) *LLMService {
	return &LLMService{cfg: cfg, keys: keys, confirm: confirm}
}

// LLMCapabilitiesDTO is the capability set of one provider.
type LLMCapabilitiesDTO struct {
	StructuredOutput bool `json:"structuredOutput"`
	Tools            bool `json:"tools"`
	MaxContext       int  `json:"maxContext"`
}

// LLMProviderDTO is one provider as the UI shows it. It never includes the key:
// HasKey only says whether one is available.
type LLMProviderDTO struct {
	Name         string             `json:"name"`
	BaseURL      string             `json:"baseURL"`
	Model        string             `json:"model"`
	Kind         string             `json:"kind"`
	Enabled      bool               `json:"enabled"`
	Capabilities LLMCapabilitiesDTO `json:"capabilities"`
	IsDefault    bool               `json:"isDefault"`
	HasKey       bool               `json:"hasKey"`
}

// LLMProviderInput is what SaveProvider accepts. APIKey is write-only: an empty
// APIKey keeps the stored key. The name identifies the provider and cannot be
// changed by an update.
type LLMProviderInput struct {
	Name         string             `json:"name"`
	BaseURL      string             `json:"baseURL"`
	Model        string             `json:"model"`
	Kind         string             `json:"kind"`
	Enabled      bool               `json:"enabled"`
	Capabilities LLMCapabilitiesDTO `json:"capabilities"`
	APIKey       string             `json:"apiKey"`
}

// ProviderTest is the outcome of TestProvider. Message never contains a key or
// any request content.
type ProviderTest struct {
	OK       bool   `json:"ok"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

// Extra categories beyond the llm package's, for the Go-side checks. The
// keyring category is CategoryKeyringDown in services.go.
const (
	CategoryCloudDisabled = "cloud_disabled"
	CategoryNoModel       = "no_model"
)

// ListProviders returns every configured provider, the default one marked. It
// reads the keyring only to see whether a key exists.
func (s *LLMService) ListProviders() ([]LLMProviderDTO, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	out := make([]LLMProviderDTO, 0, len(cfg.LLM.Providers))
	for _, p := range cfg.LLM.Providers {
		_, kerr := s.keys.GetLLMKey(p.Name)
		out = append(out, LLMProviderDTO{
			Name:         p.Name,
			BaseURL:      p.BaseURL,
			Model:        p.Model,
			Kind:         p.EffectiveKind(),
			Enabled:      p.IsEnabled(),
			Capabilities: capsDTO(p.Capabilities),
			IsDefault:    p.Name == cfg.LLM.DefaultProvider,
			HasKey:       kerr == nil,
		})
	}
	return out, nil
}

// SaveProvider creates or updates a provider.
//
// Enabling a cloud provider (disabled to enabled, a new cloud provider, or a
// change to cloud while enabled) first needs the user's confirmation in the
// native dialog. Disabling never does. Without a dialog the change is refused.
//
// A key is used only with the base URL it was approved for (KeyBinding):
//   - a non-empty apiKey replaces the stored key, with the new binding;
//   - an empty apiKey keeps a key only if the provider is bound to the new base
//     URL. This applies to keyring and environment keys, and to new providers
//     too. Otherwise the call fails with ErrKeyNeedsReentry.
//
// Write order: the keyring first, then config.json. If the config save fails,
// the keyring goes back to its previous state.
func (s *LLMService) SaveProvider(in LLMProviderInput) error {
	name := strings.TrimSpace(in.Name)
	baseURL := strings.TrimSpace(in.BaseURL)
	switch {
	case !providerNameRE.MatchString(name):
		return fmt.Errorf("%w: name must be 1 to %d letters, digits, '_' or '-', starting with a letter or digit", ErrInvalidInput, maxProviderName)
	case len(in.Model) > maxProviderModel:
		return fmt.Errorf("%w: model", ErrInvalidInput)
	case len(in.APIKey) > maxAPIKeyLen:
		return fmt.Errorf("%w: apiKey is too long", ErrInvalidInput)
	}
	targetKind := (config.LLMProvider{Kind: in.Kind, BaseURL: baseURL}).EffectiveKind()

	// Decide on the confirmation from a snapshot. The dialog runs without the
	// lock, so the decision is checked again under the lock below.
	pre, err := s.loadConfig()
	if err != nil {
		return err
	}
	preP, preExists := providerNamed(pre, name)
	needsConfirm := enablesCloud(preP, preExists, targetKind, in.Enabled)
	if needsConfirm {
		if s.confirm == nil {
			return ErrEnableDeclined
		}
		ok, cerr := s.confirm.ConfirmCloudEnable(context.Background(), name, baseURL)
		if cerr != nil {
			return fmt.Errorf("%w: %v", ErrEnableDeclined, cerr)
		}
		if !ok {
			return ErrEnableDeclined
		}
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	cur, err := s.loadConfig()
	if err != nil {
		return err
	}
	curP, curExists := providerNamed(cur, name)
	if enablesCloud(curP, curExists, targetKind, in.Enabled) && !needsConfirm {
		// The provider became an enabled cloud provider while the dialog was not
		// shown for it. Ask again rather than enable it silently.
		return ErrProviderChanged
	}

	// Key decisions. A keyring failure other than "no entry" stops here, before
	// anything is written.
	_, kerr := s.keys.GetLLMKey(name) // only "is there a key?" matters; the value is dropped
	if err := keyReadErr(kerr); err != nil {
		return err
	}
	hasKey := kerr == nil
	writeKey := in.APIKey != ""
	binding := ""
	switch {
	case writeKey:
		binding = config.BaseURLBinding(baseURL)
	case hasKey:
		if !curExists || curP.KeyBinding == "" || curP.KeyBinding != config.BaseURLBinding(baseURL) {
			return ErrKeyNeedsReentry
		}
		binding = curP.KeyBinding
	}

	cfg := copyConfig(cur)
	p := config.LLMProvider{Name: name}
	idx := findProvider(cfg, name)
	if idx >= 0 {
		p = cfg.LLM.Providers[idx]
	}
	enabled := in.Enabled
	p.BaseURL = baseURL
	p.Model = strings.TrimSpace(in.Model)
	p.Kind = in.Kind
	p.Enabled = &enabled
	p.Capabilities = capsFromDTO(in.Capabilities)
	p.KeyBinding = binding
	if idx >= 0 {
		cfg.LLM.Providers[idx] = p
	} else {
		cfg.LLM.Providers = append(cfg.LLM.Providers, p)
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	if !writeKey {
		if err := s.cfg.Save(cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		return nil
	}

	// Keyring first (replace), then config. If the config save fails, the keyring
	// goes back to what it was.
	prev, perr := s.keys.StoredLLMKey(name)
	if err := keyReadErr(perr); err != nil {
		return err
	}
	hadStored := perr == nil
	if err := s.keys.SetLLMKey(name, in.APIKey); err != nil {
		return fmt.Errorf("store API key: %w", err)
	}
	if err := s.cfg.Save(cfg); err != nil {
		if rerr := s.restoreKey(name, prev, hadStored); rerr != nil {
			return fmt.Errorf("save config: %w (restoring the previous API key also failed: %v)", err, rerr)
		}
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// restoreKey puts the keyring entry back: the previous value if there was one,
// otherwise no entry.
func (s *LLMService) restoreKey(name, prev string, hadStored bool) error {
	if hadStored {
		return s.keys.SetLLMKey(name, prev)
	}
	return s.keys.DeleteLLMKey(name)
}

// enablesCloud reports whether a save makes a provider an enabled cloud
// provider that was not one before. Only that change needs confirmation.
func enablesCloud(prev config.LLMProvider, existed bool, targetKind string, targetEnabled bool) bool {
	if targetKind != config.KindCloud || !targetEnabled {
		return false
	}
	wasCloudEnabled := existed && prev.EffectiveKind() == config.KindCloud && prev.IsEnabled()
	return !wasCloudEnabled
}

func providerNamed(cfg config.Config, name string) (config.LLMProvider, bool) {
	if idx := findProvider(cfg, name); idx >= 0 {
		return cfg.LLM.Providers[idx], true
	}
	return config.LLMProvider{}, false
}

// keyReadErr returns nil for "found" and "no entry", and a keyring-unavailable
// error for anything else. Anything else must stop a save: the call cannot tell
// whether a key is there, so it must not guess.
func keyReadErr(err error) error {
	if err == nil || errors.Is(err, secrets.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%w: %v", secrets.ErrKeyringUnavailable, err)
}

// DeleteProvider removes a provider and its keyring key. The config is written
// first, and restored if the keyring delete fails. Clearing the default is part
// of the same change.
func (s *LLMService) DeleteProvider(name string) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	old, err := s.loadConfig()
	if err != nil {
		return err
	}
	idx := findProvider(old, name)
	if idx < 0 {
		return ErrProviderNotFound
	}
	cfg := copyConfig(old)
	cfg.LLM.Providers = append(cfg.LLM.Providers[:idx], cfg.LLM.Providers[idx+1:]...)
	if cfg.LLM.DefaultProvider == name {
		cfg.LLM.DefaultProvider = ""
	}
	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := s.keys.DeleteLLMKey(name); err != nil {
		if rerr := s.cfg.Save(old); rerr != nil {
			return fmt.Errorf("delete API key: %w (restoring the previous config also failed: %v)", err, rerr)
		}
		return fmt.Errorf("delete API key: %w", err)
	}
	return nil
}

// SetDefaultProvider makes name the default provider. An empty name clears the
// default.
func (s *LLMService) SetDefaultProvider(name string) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	cfg, err := s.loadConfig()
	if err != nil {
		return err
	}
	if name != "" && findProvider(cfg, name) < 0 {
		return ErrProviderNotFound
	}
	cfg.LLM.DefaultProvider = name
	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// TestProvider checks the provider with one tiny structured call. It returns a
// category and a message. It returns an error only if the settings cannot be
// read.
func (s *LLMService) TestProvider(ctx context.Context, name string) (ProviderTest, error) {
	client, err := s.clientFor(name)
	if err != nil {
		if errors.Is(err, ErrProviderNotFound) {
			return ProviderTest{}, err
		}
		return categorizeLLM(err), nil
	}
	ctx, cancel := context.WithTimeout(ctx, llmCallTimeout)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		return categorizeLLM(err), nil
	}
	return ProviderTest{OK: true, Category: string(llm.CategoryOK), Message: "The provider answered in the requested format."}, nil
}

// ListModels returns the model IDs the provider lists, sorted. An endpoint that
// does not list models returns an empty list and an error whose message says
// so. The UI then asks for the model name.
func (s *LLMService) ListModels(ctx context.Context, name string) ([]string, error) {
	client, err := s.clientFor(name)
	if err != nil {
		return []string{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, llmCallTimeout)
	defer cancel()
	ids, err := client.ListModels(ctx)
	if err != nil {
		return []string{}, err
	}
	return ids, nil
}

// clientFor builds an llm client for a configured provider. The config and the
// key are read together under cfgMu, so a writer that is half done cannot be
// seen: the key is never paired with a base URL it was not saved for. The lock
// is released before the client is returned, so no network call runs under it.
// A key that is not bound to the current base URL is refused with
// ErrKeyNeedsReentry.
func (s *LLMService) clientFor(name string) (*llm.Client, error) {
	s.cfgMu.Lock()
	cfg, err := s.loadConfig()
	if err != nil {
		s.cfgMu.Unlock()
		return nil, err
	}
	idx := findProvider(cfg, name)
	if idx < 0 {
		s.cfgMu.Unlock()
		return nil, ErrProviderNotFound
	}
	prov := cfg.LLM.Providers[idx]
	key, kerr := s.keys.GetLLMKey(name)
	s.cfgMu.Unlock()

	if err := keyReadErr(kerr); err != nil {
		return nil, err
	}
	if kerr == nil {
		if !prov.KeyBoundTo() {
			return nil, ErrKeyNeedsReentry
		}
	} else {
		key = "" // no key: a local endpoint such as Ollama
	}
	client, err := llm.NewClient(llm.ProviderFrom(prov), key, llm.Options{Timeout: llmCallTimeout})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return client, nil
}

func (s *LLMService) loadConfig() (config.Config, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func findProvider(cfg config.Config, name string) int {
	for i, p := range cfg.LLM.Providers {
		if p.Name == name {
			return i
		}
	}
	return -1
}

func capsDTO(c config.LLMCapabilities) LLMCapabilitiesDTO {
	return LLMCapabilitiesDTO{StructuredOutput: c.StructuredOutput, Tools: c.Tools, MaxContext: c.MaxContext}
}

func capsFromDTO(c LLMCapabilitiesDTO) config.LLMCapabilities {
	return config.LLMCapabilities{StructuredOutput: c.StructuredOutput, Tools: c.Tools, MaxContext: c.MaxContext}
}

// categorizeLLM maps an error from the llm package to a ProviderTest.
func categorizeLLM(err error) ProviderTest {
	if err == nil {
		return ProviderTest{OK: true, Category: string(llm.CategoryOK), Message: "The provider answered in the requested format."}
	}
	cat, msg := string(llm.CategoryOf(err)), ""
	switch {
	case errors.Is(err, llm.ErrCloudDisabled):
		cat, msg = CategoryCloudDisabled, "This cloud provider is turned off. Enable it before testing or sending content."
	case errors.Is(err, llm.ErrNoModel):
		cat, msg = CategoryNoModel, "Set a model name for this provider first."
	case errors.Is(err, secrets.ErrKeyringUnavailable):
		cat, msg = CategoryKeyringDown, "The OS keychain is not available, so the API key cannot be read."
	case errors.Is(err, llm.ErrInvalidOutput):
		cat, msg = string(llm.CategoryStructuredUnsupported), "The provider did not return the structured format. Try turning off structuredOutput for it."
	default:
		msg = llmMessage(llm.CategoryOf(err))
	}
	return ProviderTest{OK: false, Category: cat, Message: msg}
}

func llmMessage(c llm.Category) string {
	switch c {
	case llm.CategoryUnreachable:
		return "The provider could not be reached. Check the base URL and that the service is running."
	case llm.CategoryUnauthorized:
		return "The provider rejected the API key."
	case llm.CategoryModelNotFound:
		return "The model or the endpoint path was not found."
	case llm.CategoryStructuredUnsupported:
		return "The provider does not return the structured format. Try turning off structuredOutput for it."
	case llm.CategoryTimeout:
		return "The provider did not answer in time."
	default:
		return "The provider request failed."
	}
}
