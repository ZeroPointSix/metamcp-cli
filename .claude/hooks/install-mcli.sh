#!/bin/sh
# SessionStart hook: make sure mcli is installed and pointed at the MetaMCP endpoint.
# The API key is never stored here; it comes from the MCLI_API_KEY environment secret.
set -u

MCLI_ENDPOINT="https://mcp.zerodotsixai.com/metamcp/zerodotsix/mcp"
export PATH="$HOME/.local/bin:$PATH"

if ! command -v mcli >/dev/null 2>&1; then
  curl -fsSL https://raw.githubusercontent.com/ZeroPointSix/metamcp-cli/main/install.sh | sh >/dev/null 2>&1 \
    || { echo "mcli install failed; install manually with install.sh from ZeroPointSix/metamcp-cli"; exit 0; }
fi

if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
  echo "export PATH=\"$HOME/.local/bin:\$PATH\"" >> "$CLAUDE_ENV_FILE"
  [ -n "${MCLI_URL:-}" ] || echo "export MCLI_URL=\"$MCLI_ENDPOINT\"" >> "$CLAUDE_ENV_FILE"
fi

if [ -z "${MCLI_API_KEY:-}" ] && ! mcli config show 2>/dev/null | grep -q '"api_key": "\*'; then
  echo "mcli is installed but has no API key: set the MCLI_API_KEY environment secret, or pipe the credstore key agent.production.metamcp_api_key into 'mcli config set --api-key-stdin'."
fi
exit 0
