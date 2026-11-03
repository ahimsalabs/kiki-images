# The agent image's environment for login shells, whose /etc/profile
# replaces PATH. Commands kiki runs get the same from the image config.
export PATH="$HOME/.cargo/bin:$HOME/.npm-global/bin:$HOME/.bun/bin:$HOME/go/bin:$HOME/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
export NPM_CONFIG_PREFIX="$HOME/.npm-global"
export PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
export AGENT_BROWSER_EXECUTABLE_PATH=/usr/local/bin/chrome-headless-shell
