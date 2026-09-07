package config

import (
	"os"
	"strings"
	"testing"
)

// A guard against somebody helpfully giving a secret a default.
//
// It reads the source of this very package. If the line that requires one of these ever becomes an
// `optional` with a fallback, this fails and says why — which is the only way to stop a default
// creeping in during a hurried debugging session and never being taken out again.
func TestSecretsAreNeverDefaulted(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MONGO_URI", "KMS_KEY_ARN", "AWS_REGION"} {
		if !strings.Contains(string(src), `require("`+name+`")`) {
			t.Errorf("%s must be read with require(), never defaulted", name)
		}
	}
}

// Invariant I8. A single global endpoint is the one variable capable of sending a sandbox payment
// to mainnet, so it does not exist — and this makes sure it does not quietly come back.
func TestThereIsNoGenericRpcUrl(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), `"RPC_URL"`) {
		t.Error("a generic RPC_URL appeared. The endpoint is chosen per record (I8)")
	}
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{
		"MONGO_URI", "MONGO_DB", "NETWORKS", "LOG_LEVEL",
		"RPC_SANDBOX_URL", "RPC_MAINNET_URL", "USDC_MINT_SANDBOX", "USDC_MINT_MAINNET",
		"KMS_KEY_ARN", "AWS_REGION", "KMS_LOCAL", "SIGNER_SNAPSHOT_TTL",
		"ALERTS_ENABLED", "TELEGRAM_BOT_TOKEN", "JOB_ALERT_EVAL", "DEMO402_PAY_TO",
	} {
		os.Unsetenv(k)
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func sandboxBase() map[string]string {
	return map[string]string{
		"MONGO_URI":         "mongodb://localhost:27017/leash?replicaSet=rs0",
		"NETWORKS":          "sandbox",
		"RPC_SANDBOX_URL":   "https://api.devnet.solana.com",
		"USDC_MINT_SANDBOX": "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
		"KMS_LOCAL":         "true",
	}
}

func TestASandboxSignerBoots(t *testing.T) {
	setEnv(t, sandboxBase())
	s, err := LoadSigner()
	if err != nil {
		t.Fatalf("a fully configured sandbox signer should boot: %v", err)
	}
	if len(s.Networks) != 1 || s.RPC[s.Networks[0]].Primary == "" {
		t.Errorf("per-network configuration did not load: %+v", s)
	}
}

// The half-configured rule. Mainnet enabled with no mainnet endpoint must not boot pointing
// somewhere else — that is the exact failure I8 exists to prevent.
func TestMainnetWithoutItsEndpointIsABootFailure(t *testing.T) {
	env := sandboxBase()
	env["NETWORKS"] = "sandbox,mainnet"
	env["KMS_LOCAL"] = "false"
	env["KMS_KEY_ARN"] = "arn:aws:kms:eu-west-1:1:key/abc"
	env["AWS_REGION"] = "eu-west-1"
	setEnv(t, env)

	_, err := LoadSigner()
	if err == nil {
		t.Fatal("mainnet with no RPC_MAINNET_URL booted")
	}
	for _, want := range []string{"RPC_MAINNET_URL", "USDC_MINT_MAINNET", "NETWORKS includes mainnet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %s:\n%v", want, err)
		}
	}
}

// Every missing variable at once. Fixing one per restart is a bad afternoon.
func TestEveryMissingVariableIsNamedTogether(t *testing.T) {
	setEnv(t, map[string]string{"NETWORKS": "sandbox", "KMS_LOCAL": "true"})
	_, err := LoadSigner()
	if err == nil {
		t.Fatal("an unconfigured signer booted")
	}
	for _, want := range []string{"MONGO_URI", "RPC_SANDBOX_URL", "USDC_MINT_SANDBOX"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name %s in one go:\n%v", want, err)
		}
	}
}

// A locally-wrapped agent key protecting real money is exactly the shortcut that should not be one
// flag away from happening.
func TestLocalKeyWrappingIsRefusedOnMainnet(t *testing.T) {
	env := sandboxBase()
	env["NETWORKS"] = "sandbox,mainnet"
	env["RPC_MAINNET_URL"] = "https://api.mainnet-beta.solana.com"
	env["USDC_MINT_MAINNET"] = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	env["KMS_LOCAL"] = "true"
	setEnv(t, env)

	_, err := LoadSigner()
	if err == nil {
		t.Fatal("KMS_LOCAL was accepted alongside mainnet")
	}
	if !strings.Contains(err.Error(), "KMS_LOCAL") {
		t.Errorf("the error should explain the combination:\n%v", err)
	}
}

// Rule S1 permits a 60-second cache. A configuration that exceeds it would make the interface's
// claim about liveness indefensible, so it is refused rather than honoured.
func TestASnapshotCacheLongerThanS1PermitsIsRefused(t *testing.T) {
	env := sandboxBase()
	env["SIGNER_SNAPSHOT_TTL"] = "5m"
	setEnv(t, env)
	if _, err := LoadSigner(); err == nil {
		t.Fatal("a 5-minute snapshot cache was accepted")
	}
}

// The interface promises alerts within thirty seconds. A slower cadence would make that false.
func TestASlowerAlertCadenceThanPromisedIsRefused(t *testing.T) {
	env := sandboxBase()
	env["JOB_ALERT_EVAL"] = "2m"
	setEnv(t, env)
	if _, err := LoadIndexer(); err == nil {
		t.Fatal("a two-minute alert cadence was accepted")
	}
}

func TestAlertsEnabledWithoutATokenIsABootFailure(t *testing.T) {
	env := sandboxBase()
	env["ALERTS_ENABLED"] = "true"
	setEnv(t, env)
	_, err := LoadIndexer()
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Fatalf("alerts enabled with no token should fail naming the token: %v", err)
	}
}

// The sample endpoint is a sandbox fixture. Refusing at boot is better than refusing per request.
func TestTheSampleEndpointRefusesMainnet(t *testing.T) {
	env := sandboxBase()
	env["NETWORKS"] = "sandbox,mainnet"
	env["RPC_MAINNET_URL"] = "https://api.mainnet-beta.solana.com"
	env["USDC_MINT_MAINNET"] = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	env["DEMO402_PAY_TO"] = "Ex4Y68L2wRXsQ33x5oUz49KwjQ69ZCcwY99MsszehEdF"
	setEnv(t, env)
	if _, err := LoadDemo402(); err == nil {
		t.Fatal("the sample endpoint accepted mainnet")
	}
}
