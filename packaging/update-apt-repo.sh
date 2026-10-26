#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="${REPO_DIR:-public}"
DEB_DIR="${DEB_DIR:-dist}"
BASE_URL="${BASE_URL:-https://huadaox.github.io/shareto}"
SIGN_KEY="${GPG_KEY_ID:-}"

echo "===> Preparing APT repository in: ${REPO_DIR}"
mkdir -p "${REPO_DIR}/conf"

# 1. Ensure GPG signing key is available
if [ -z "${SIGN_KEY}" ]; then
    echo "No GPG_KEY_ID specified. Checking existing GPG keys..."
    SIGN_KEY="$(gpg --list-secret-keys --with-colons 2>/dev/null | awk -F: '/^sec:/ {print $5; exit}')" || true
    if [ -z "${SIGN_KEY}" ]; then
        echo "No existing GPG key found. Generating a dedicated signing key for APT repository..."
        KEY_PARAM=$(mktemp)
        cat <<EOF > "${KEY_PARAM}"
%echo Generating APT signing key
Key-Type: RSA
Key-Length: 4096
Subkey-Type: RSA
Subkey-Length: 4096
Name-Real: shareto APT Repository
Name-Email: support@shareto.local
Expire-Date: 0
%no-ask-passphrase
%no-protection
%commit
%echo Done
EOF
        gpg --batch --generate-key "${KEY_PARAM}"
        rm -f "${KEY_PARAM}"
        SIGN_KEY="$(gpg --list-secret-keys --with-colons 2>/dev/null | awk -F: '/^sec:/ {print $5; exit}')"
    fi
fi

echo "Using GPG key: ${SIGN_KEY}"
gpg --armor --export "${SIGN_KEY}" > "${REPO_DIR}/key.gpg"

# 2. Configure reprepro distributions
cat <<EOF > "${REPO_DIR}/conf/distributions"
Origin: shareto
Label: shareto
Codename: stable
Suite: stable
Architectures: amd64 arm64
Components: main
Description: Official APT repository for shareto (Safe and minimal large file distribution tool for Linux)
SignWith: ${SIGN_KEY}
EOF

# 3. Add .deb packages to reprepro
shopt -s nullglob
DEB_FILES=("${DEB_DIR}"/*.deb)
shopt -u nullglob

if [ ${#DEB_FILES[@]} -eq 0 ]; then
    echo "Warning: No .deb files found in ${DEB_DIR}"
else
    echo "Adding deb packages to repository..."
    for deb in "${DEB_FILES[@]}"; do
        echo "Processing ${deb}..."
        reprepro -b "${REPO_DIR}" --ignore=alreadyincluded --ignore=missingfield includedeb stable "${deb}"
    done
fi

# 4. Generate one-line installer script (install.sh)
cat <<'EOF' > "${REPO_DIR}/install.sh"
#!/usr/bin/env bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
    echo "This script must be run as root (e.g. using sudo)." >&2
    exit 1
fi

echo "===> Setting up shareto APT repository..."

# Install prerequisite tools
apt-get update -y
apt-get install -y --no-install-recommends ca-certificates curl gnupg

# Add GPG keyring
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://huadaox.github.io/shareto/key.gpg | gpg --dearmor --yes -o /etc/apt/keyrings/shareto.gpg
chmod a+r /etc/apt/keyrings/shareto.gpg

# Add sources list
echo "deb [signed-by=/etc/apt/keyrings/shareto.gpg] https://huadaox.github.io/shareto stable main" | tee /etc/apt/sources.list.d/shareto.list > /dev/null

echo "===> Installing shareto..."
apt-get update -y
apt-get install -y shareto

echo ""
echo "Successfully installed shareto!"
echo "Run 'shareto init' to get started."
EOF
chmod +x "${REPO_DIR}/install.sh"

# 5. Generate index.html for GitHub Pages
cat <<EOF > "${REPO_DIR}/index.html"
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>shareto APT Repository</title>
  <style>
    :root {
      --bg: #0f172a;
      --card-bg: #1e293b;
      --text: #f8fafc;
      --muted: #94a3b8;
      --primary: #38bdf8;
      --accent: #22c55e;
      --border: #334155;
      --code-bg: #0b1120;
    }
    body {
      margin: 0;
      padding: 0;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
      background-color: var(--bg);
      color: var(--text);
      display: flex;
      justify-content: center;
      align-items: center;
      min-height: 100vh;
      box-sizing: border-box;
    }
    .container {
      width: 100%;
      max-width: 760px;
      margin: 40px 20px;
      background: var(--card-bg);
      border: 1px solid var(--border);
      border-radius: 12px;
      padding: 36px 32px;
      box-shadow: 0 20px 25px -5px rgba(0, 0, 0, 0.5);
    }
    h1 {
      margin-top: 0;
      font-size: 2rem;
      display: flex;
      align-items: center;
      gap: 12px;
      color: var(--text);
    }
    h1 .badge {
      font-size: 0.875rem;
      background-color: rgba(56, 189, 248, 0.15);
      color: var(--primary);
      padding: 4px 10px;
      border-radius: 9999px;
      border: 1px solid rgba(56, 189, 248, 0.3);
    }
    p.desc {
      color: var(--muted);
      font-size: 1.05rem;
      line-height: 1.6;
    }
    h2 {
      font-size: 1.25rem;
      margin-top: 28px;
      margin-bottom: 12px;
      color: var(--primary);
    }
    pre {
      background: var(--code-bg);
      padding: 16px;
      border-radius: 8px;
      border: 1px solid var(--border);
      overflow-x: auto;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
      font-size: 0.9rem;
      line-height: 1.5;
      color: #e2e8f0;
      position: relative;
    }
    .footer {
      margin-top: 32px;
      padding-top: 16px;
      border-top: 1px solid var(--border);
      display: flex;
      justify-content: space-between;
      color: var(--muted);
      font-size: 0.875rem;
    }
    a {
      color: var(--primary);
      text-decoration: none;
    }
    a:hover {
      text-decoration: underline;
    }
  </style>
</head>
<body>
  <div class="container">
    <h1>shareto <span class="badge">APT Repository</span></h1>
    <p class="desc">
      Official Debian / Ubuntu APT repository for <strong>shareto</strong> — a safe and minimal large file distribution tool for Linux.
    </p>

    <h2>Quick Install (One-liner)</h2>
    <pre><code>curl -fsSL https://huadaox.github.io/shareto/install.sh | sudo bash</code></pre>

    <h2>Manual APT Setup</h2>
    <p class="desc">1. Import GPG public key:</p>
    <pre><code>sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://huadaox.github.io/shareto/key.gpg | sudo gpg --dearmor --yes -o /etc/apt/keyrings/shareto.gpg
sudo chmod a+r /etc/apt/keyrings/shareto.gpg</code></pre>

    <p class="desc">2. Add the repository to your APT sources:</p>
    <pre><code>echo "deb [signed-by=/etc/apt/keyrings/shareto.gpg] https://huadaox.github.io/shareto stable main" | sudo tee /etc/apt/sources.list.d/shareto.list &gt; /dev/null</code></pre>

    <p class="desc">3. Update and install shareto:</p>
    <pre><code>sudo apt update &amp;&amp; sudo apt install -y shareto</code></pre>

    <div class="footer">
      <span>Architectures: <code>amd64</code>, <code>arm64</code></span>
      <span><a href="https://github.com/huadaox/shareto" target="_blank" rel="noopener">GitHub Project</a></span>
    </div>
  </div>
</body>
</html>
EOF

echo "===> APT repository successfully updated at ${REPO_DIR}"
