# check=skip=InvalidDefaultArgInFrom
# kiki's agent image: Ubuntu 26.04 with systemd, run as exedev, with the
# tools agents use. Every input is pinned in versions.env (build.sh passes
# it as build args) and every download is checked against its sha256.
#
# Each tool is fetched in a stage of its own into /out and copied into the
# image with COPY --link, which makes its layer independent of the layers
# beneath it. Built with SOURCE_DATE_EPOCH and rewrite-timestamp (build.sh's
# callers pass it to the exporter), a layer's bytes are a function of its
# inputs: moving the apt snapshot changes the apt and Docker layers, a Go
# bump the Go layer, and a host pulls only those.

ARG UBUNTU_VERSION UBUNTU_DIGEST

# --- fetch: what the tool stages download and unpack with ------------------

FROM ubuntu:${UBUNTU_VERSION}@${UBUNTU_DIGEST} AS fetch
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
# Only tools to download with; nothing of this stage is in the image but
# what the stages after it write to /out, each file checked by its sha256.
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	if [[ -s /run/secrets/ca ]]; then \
		echo 'Acquire::https::CAInfo "/run/secrets/ca";' > /etc/apt/apt.conf.d/99build-ca; \
	fi && \
	apt-get update -o APT::Update::Error-Mode=any && \
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		ca-certificates curl python3 unzip xz-utils && \
	rm -rf /var/lib/apt/lists/*
COPY fetch.sh rust-dist.sh rootfs/usr/local/lib/kiki-agent/arch /usr/local/lib/kiki-agent/
ENV PATH=/usr/local/lib/kiki-agent:$PATH

# --- one stage per tool -----------------------------------------------------
# "arch STYLE" names the build's architecture as the release does. A build
# behind a TLS-intercepting proxy mounts its CA as the secret "ca".

FROM fetch AS go
ARG TARGETARCH GO_VERSION GO_SHA256_amd64 GO_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=GO_SHA256_${TARGETARCH} && \
	fetch.sh "https://go.dev/dl/go${GO_VERSION}.linux-$(arch go).tar.gz" "${!sha}" /out/usr/local && \
	rm -rf /out/usr/local/go/test /out/usr/local/go/api && \
	mkdir -p /out/usr/local/bin && \
	ln -s /usr/local/go/bin/go /usr/local/go/bin/gofmt /out/usr/local/bin/

FROM fetch AS node
ARG TARGETARCH NODE_VERSION NODE_SHA256_amd64 NODE_SHA256_arm64
# corepack's pnpm and yarn shims link to it relatively, so they hold in /.
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=NODE_SHA256_${TARGETARCH} && \
	fetch.sh "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-$(arch node).tar.xz" "${!sha}" /out/usr/local/node 1 && \
	mkdir -p /out/usr/local/bin && \
	for cmd in node npm npx corepack; do ln -s "/usr/local/node/bin/$cmd" /out/usr/local/bin/; done && \
	/out/usr/local/node/bin/node /out/usr/local/node/bin/corepack enable --install-directory /out/usr/local/bin pnpm yarn && \
	find /out/usr/local/bin -lname '/out/*' -printf '%p -> %l\n' | grep . && exit 1 || true

FROM fetch AS bun
ARG TARGETARCH BUN_VERSION BUN_SHA256_amd64 BUN_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=BUN_SHA256_${TARGETARCH} && a=$(arch bun) && \
	fetch.sh "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${a}.zip" "${!sha}" /out/usr/local/bun && \
	mkdir -p /out/usr/local/bin && \
	ln -s "/usr/local/bun/bun-linux-${a}/bun" /out/usr/local/bin/bun && \
	ln -s bun /out/usr/local/bin/bunx

FROM fetch AS jj
ARG TARGETARCH JJ_VERSION JJ_SHA256_amd64 JJ_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=JJ_SHA256_${TARGETARCH} && \
	fetch.sh "https://github.com/jj-vcs/jj/releases/download/v${JJ_VERSION}/jj-v${JJ_VERSION}-$(arch gnu)-unknown-linux-musl.tar.gz" "${!sha}" /out/usr/local/jj && \
	mkdir -p /out/usr/local/bin && ln -s /usr/local/jj/jj /out/usr/local/bin/jj

FROM fetch AS task
ARG TARGETARCH TASK_VERSION TASK_SHA256_amd64 TASK_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=TASK_SHA256_${TARGETARCH} && \
	fetch.sh "https://github.com/go-task/task/releases/download/v${TASK_VERSION}/task_linux_$(arch go).tar.gz" "${!sha}" /out/usr/local/task && \
	mkdir -p /out/usr/local/bin && ln -s /usr/local/task/task /out/usr/local/bin/task

FROM fetch AS gh
ARG TARGETARCH GH_VERSION GH_SHA256_amd64 GH_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=GH_SHA256_${TARGETARCH} && \
	fetch.sh "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_$(arch go).tar.gz" "${!sha}" /out/usr/local/gh 1 && \
	mkdir -p /out/usr/local/bin && ln -s /usr/local/gh/bin/gh /out/usr/local/bin/gh

FROM fetch AS ripgrep
ARG TARGETARCH RIPGREP_VERSION RIPGREP_SHA256_amd64 RIPGREP_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=RIPGREP_SHA256_${TARGETARCH} && \
	fetch.sh "https://github.com/BurntSushi/ripgrep/releases/download/${RIPGREP_VERSION}/ripgrep-${RIPGREP_VERSION}-$(arch gnu)-unknown-linux-musl.tar.gz" "${!sha}" /out/usr/local/ripgrep 1 && \
	mkdir -p /out/usr/local/bin && ln -s /usr/local/ripgrep/rg /out/usr/local/bin/rg

FROM fetch AS uv
ARG TARGETARCH UV_VERSION UV_SHA256_amd64 UV_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=UV_SHA256_${TARGETARCH} && \
	fetch.sh "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-$(arch gnu)-unknown-linux-gnu.tar.gz" "${!sha}" /out/usr/local/uv 1 && \
	mkdir -p /out/usr/local/bin && ln -s /usr/local/uv/uv /usr/local/uv/uvx /out/usr/local/bin/

FROM fetch AS agent-browser
ARG TARGETARCH AGENT_BROWSER_VERSION AGENT_BROWSER_SHA256_amd64 AGENT_BROWSER_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=AGENT_BROWSER_SHA256_${TARGETARCH} && \
	fetch.sh "https://github.com/vercel-labs/agent-browser/releases/download/v${AGENT_BROWSER_VERSION}/agent-browser-linux-$(arch node)" "${!sha}" /out/usr/local/bin/agent-browser

# One headless Chromium for Playwright and agent-browser: where the pinned
# playwright-core looks for its headless shell (PLAYWRIGHT_BROWSERS_PATH),
# with the markers its installer leaves, and as chromium on PATH.
FROM fetch AS chrome
ARG TARGETARCH PLAYWRIGHT_VERSION CHROME_REVISION CHROME_VERSION CHROME_SHA256_amd64 CHROME_SHA256_arm64
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=CHROME_SHA256_${TARGETARCH} && a=$(arch cft) && \
	dir=/opt/ms-playwright/chromium_headless_shell-${CHROME_REVISION} && \
	fetch.sh "https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/${a}/chrome-headless-shell-${a}.zip" "${!sha}" "/out$dir" && \
	touch "/out$dir/INSTALLATION_COMPLETE" "/out$dir/DEPENDENCIES_VALIDATED" && \
	chmod -R a+rX /out/opt/ms-playwright && \
	mkdir -p /out/usr/local/bin && \
	ln -s "$dir/chrome-headless-shell-${a}/chrome-headless-shell" /out/usr/local/bin/chrome-headless-shell && \
	ln -s chrome-headless-shell /out/usr/local/bin/chromium

# Rust: rustup-init and the stable toolchain's dist files (compressed, about
# a fifth of the installed size). cargo, rustc and the rest install the
# toolchain for their user from /opt/rust-dist on first use, offline.
FROM fetch AS rust
ARG TARGETARCH RUSTUP_VERSION RUSTUP_SHA256_amd64 RUSTUP_SHA256_arm64 RUST_VERSION RUST_MANIFEST_SHA256
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	sha=RUSTUP_SHA256_${TARGETARCH} && target=$(arch gnu)-unknown-linux-gnu && \
	fetch.sh "https://static.rust-lang.org/rustup/archive/${RUSTUP_VERSION}/${target}/rustup-init" "${!sha}" /out/usr/local/lib/kiki-agent/rustup-init && \
	rust-dist.sh "${RUST_VERSION}" "${RUST_MANIFEST_SHA256}" "$target" /out/opt/rust-dist && \
	mkdir -p /out/usr/local/bin && \
	for cmd in cargo cargo-clippy cargo-fmt clippy-driver rustc rustdoc rustfmt rustup; do \
		ln -s /usr/local/lib/kiki-agent/rust-first-use "/out/usr/local/bin/$cmd"; \
	done

# --- the image --------------------------------------------------------------

FROM ubuntu:${UBUNTU_VERSION}@${UBUNTU_DIGEST} AS image
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]
ARG TARGETARCH
ARG APT_SNAPSHOT

# Packages, from the Ubuntu archive as it was at APT_SNAPSHOT. The snapshot
# service is https-only and the base has no CA certificates, so
# ca-certificates comes first from the live archive, then is replaced by the
# snapshot's version. A build behind a TLS-intercepting proxy passes its CA
# bundle as the build secret "ca" (build.sh does); it is never in a layer.
# GNU coreutils replaces 26.04's default uutils, so head, ls, cat and the
# rest behave as agents expect them to; an apt pin keeps later installs
# (here or in a VM) from switching back. That rules out 26.04's
# build-essential, which depends on uutils, so its dependencies are named
# instead: gcc, g++, make, libc6-dev and libcrypt-dev. Every package is from
# Ubuntu's main component, which Ubuntu's security team maintains. What an
# install writes that differs from build to build (dpkg and apt logs,
# ldconfig's aux-cache, fontconfig's caches, which record directory times
# that rewrite-timestamp then changes, and a machine-id) is removed in the
# same layer, so the layer is a function of the snapshot alone.
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	if [[ -s /run/secrets/ca ]]; then \
		echo 'Acquire::https::CAInfo "/run/secrets/ca";' > /etc/apt/apt.conf.d/99build-ca; \
	fi && \
	export DEBIAN_FRONTEND=noninteractive && \
	apt-get update -o APT::Update::Error-Mode=any && \
	apt-get install -y --no-install-recommends ca-certificates && \
	sed -i "/^Types:/a Snapshot: ${APT_SNAPSHOT}" /etc/apt/sources.list.d/ubuntu.sources && \
	apt-get update -o APT::Update::Error-Mode=any && \
	apt-get -y --allow-downgrades dist-upgrade && \
	printf 'Package: coreutils-from-uutils rust-coreutils\nPin: release *\nPin-Priority: -1\n' \
		> /etc/apt/preferences.d/kiki-agent-gnu-coreutils && \
	apt-get install -y --allow-remove-essential coreutils-from-gnu coreutils-from-uutils- && \
	apt-get purge -y --autoremove rust-coreutils && \
	apt-get install -y --allow-downgrades --no-install-recommends \
		bind9-dnsutils \
		ca-certificates \
		curl \
		dbus \
		file \
		g++ \
		gcc \
		gawk \
		git \
		htop \
		iproute2 \
		iputils-ping \
		jq \
		less \
		libc6-dev \
		libcrypt-dev \
		libpam-systemd \
		lsof \
		make \
		netcat-openbsd \
		openssh-client \
		patch \
		pkg-config \
		procps \
		psmisc \
		python3 \
		python3-venv \
		rsync \
		sqlite3 \
		strace \
		sudo \
		systemd \
		systemd-sysv \
		tcpdump \
		tmux \
		tree \
		unzip \
		vim-tiny \
		wget \
		xz-utils \
		zip \
		zstd \
		`# chrome-headless-shell's libraries and fonts` \
		fonts-freefont-ttf \
		fonts-liberation \
		fonts-noto-cjk \
		fonts-noto-color-emoji \
		libasound2t64 \
		libatk-bridge2.0-0t64 \
		libatk1.0-0t64 \
		libatspi2.0-0t64 \
		libcairo2 \
		libcups2t64 \
		libdbus-1-3 \
		libdrm2 \
		libnspr4 \
		libnss3 \
		libpango-1.0-0 \
		libx11-6 \
		libxcb1 \
		libxcomposite1 \
		libxdamage1 \
		libxext6 \
		libxfixes3 \
		libxkbcommon0 \
		libxrandr2 && \
	rm -f /etc/apt/apt.conf.d/99build-ca && \
	rm -rf /var/lib/apt/lists/* /var/log/apt/* /var/log/dpkg.log /var/log/alternatives.log /var/cache/ldconfig/aux-cache /var/cache/fontconfig/* && \
	: > /etc/machine-id && \
	ln -sf /etc/machine-id /var/lib/dbus/machine-id

# chrome-headless-shell links libgbm.so.1. Ubuntu's libgbm1 depends on
# mesa-libgallium and LLVM, about 180 MB a headless browser never loads: GBM
# opens a Gallium driver only to create a GPU device, and the headless shell
# renders with SwiftShader. So the library alone comes from the snapshot's
# libgbm1 (apt checks it against the signed index) into a directory ld.so
# searches after the system's: installing libgbm1 in a VM takes over.
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	if [[ -s /run/secrets/ca ]]; then \
		echo 'Acquire::https::CAInfo "/run/secrets/ca";' > /etc/apt/apt.conf.d/99build-ca; \
	fi && \
	apt-get update -o APT::Update::Error-Mode=any && \
	tmp=$(mktemp -d) && cd "$tmp" && apt-get download libgbm1 && \
	dpkg-deb -x libgbm1_*.deb x && \
	mkdir -p /usr/local/lib/kiki-agent/gbm && \
	cp -a x/usr/lib/*-linux-gnu/libgbm.so.1* /usr/local/lib/kiki-agent/gbm/ && \
	echo /usr/local/lib/kiki-agent/gbm > /etc/ld.so.conf.d/zz-kiki-agent-gbm.conf && \
	cd / && rm -rf "$tmp" /etc/apt/apt.conf.d/99build-ca && \
	rm -rf /var/lib/apt/lists/* /var/log/apt/* /var/log/dpkg.log /var/log/alternatives.log /var/cache/ldconfig/aux-cache

# Docker Engine from Docker's apt repository, which a VM's apt keeps
# following. Socket-activated: dockerd starts on the first docker command,
# not at boot. kiki keeps /var/lib/docker and /var/lib/containerd off the
# overlay root in systemd mode.
ARG UBUNTU_CODENAME DOCKER_GPG_SHA256 DOCKER_CE_VERSION CONTAINERD_VERSION DOCKER_BUILDX_VERSION DOCKER_COMPOSE_VERSION
RUN --mount=type=secret,id=ca,required=false,mode=0444 \
	if [[ -s /run/secrets/ca ]]; then \
		export CURL_CA_BUNDLE=/run/secrets/ca; \
		echo 'Acquire::https::CAInfo "/run/secrets/ca";' > /etc/apt/apt.conf.d/99build-ca; \
	fi && \
	install -d -m 755 /etc/apt/keyrings && \
	curl -fsSL --retry 3 -o /etc/apt/keyrings/docker.asc https://download.docker.com/linux/ubuntu/gpg && \
	echo "${DOCKER_GPG_SHA256}  /etc/apt/keyrings/docker.asc" | sha256sum -c --quiet - && \
	printf 'Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: %s\nComponents: stable\nSigned-By: /etc/apt/keyrings/docker.asc\n' \
		"${UBUNTU_CODENAME}" > /etc/apt/sources.list.d/docker.sources && \
	apt-get update -o APT::Update::Error-Mode=any && \
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		"docker-ce=${DOCKER_CE_VERSION}" \
		"docker-ce-cli=${DOCKER_CE_VERSION}" \
		"containerd.io=${CONTAINERD_VERSION}" \
		"docker-buildx-plugin=${DOCKER_BUILDX_VERSION}" \
		"docker-compose-plugin=${DOCKER_COMPOSE_VERSION}" && \
	rm -f /etc/apt/apt.conf.d/99build-ca && \
	rm -rf /var/lib/apt/lists/* /var/log/apt/* /var/log/dpkg.log /var/log/alternatives.log /var/cache/ldconfig/aux-cache && \
	systemctl disable docker.service containerd.service && \
	systemctl enable docker.socket

COPY rootfs/ /
COPY smoke.sh /usr/local/share/kiki-agent/smoke.sh

# systemd as PID 1 of a kiki VM: mask what has no hardware, console or
# package manager to serve, as exeuntu does (see NOTICE). kiki writes the
# VM's /etc/fstab, hostname, hosts and resolv.conf itself.
RUN systemctl mask -- \
		getty.target console-getty.service \
		systemd-random-seed.service \
		etc-resolv.conf.mount etc-hosts.mount etc-hostname.mount -.mount \
		systemd-remount-fs.service \
		systemd-sysusers.service \
		systemd-update-done.service \
		systemd-update-utmp.service \
		systemd-journal-catalog-update.service \
		modprobe@.service \
		systemd-modules-load.service \
		systemd-udevd.service systemd-udevd-control.service systemd-udevd-kernel.service \
		systemd-udev-trigger.service systemd-udev-settle.service systemd-hwdb-update.service \
		ldconfig.service \
		keyboard-setup.service \
		systemd-ask-password-console.path systemd-ask-password-wall.path \
		apt-daily-upgrade.timer apt-daily.timer \
		dpkg-db-backup.timer e2scrub_all.timer && \
	for unit in systemd-logind.service systemd-firstboot.service systemd-machine-id-commit.service systemd-sysctl.service; do \
		systemctl disable "$unit" 2>/dev/null || true; \
	done && \
	systemctl set-default multi-user.target

# The login user: Ubuntu's uid 1000 renamed, as in exeuntu, with
# passwordless sudo and a lingering systemd user manager.
RUN usermod -l exedev -c "kiki agent" ubuntu && \
	groupmod -n exedev ubuntu && \
	usermod -d /home/exedev -m exedev && \
	usermod -aG sudo,docker exedev && \
	echo 'exedev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/exedev && \
	chmod 440 /etc/sudoers.d/exedev && \
	mkdir -p /var/lib/systemd/linger && \
	touch /var/lib/systemd/linger/exedev

# The tools, one layer each, independent of everything above.
COPY --link --from=go /out/ /
COPY --link --from=node /out/ /
COPY --link --from=bun /out/ /
COPY --link --from=jj /out/ /
COPY --link --from=task /out/ /
COPY --link --from=gh /out/ /
COPY --link --from=ripgrep /out/ /
COPY --link --from=uv /out/ /
COPY --link --from=agent-browser /out/ /
COPY --link --from=chrome /out/ /
COPY --link --from=rust /out/ /

ENV PATH=/home/exedev/.cargo/bin:/home/exedev/.npm-global/bin:/home/exedev/.bun/bin:/home/exedev/go/bin:/home/exedev/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
	NPM_CONFIG_PREFIX=/home/exedev/.npm-global \
	PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
	AGENT_BROWSER_EXECUTABLE_PATH=/usr/local/bin/chrome-headless-shell

# The build installs from APT_SNAPSHOT, so a rebuild gets the same
# packages; a VM's own apt-get uses the live archive, security updates
# included. ldconfig picks up the GBM directory.
# Empty, not absent: systemd reads an absent machine-id as a first boot and
# presets every unit, re-enabling the ones disabled above. Each VM gets its
# own ID on first boot. Keep this last, so no later install bakes one in.
RUN sed -i '/^Snapshot:/d' /etc/apt/sources.list.d/ubuntu.sources && \
	ldconfig && rm -f /var/cache/ldconfig/aux-cache && \
	: > /etc/machine-id && \
	ln -sf /etc/machine-id /var/lib/dbus/machine-id

LABEL exe.dev/login-user="exedev"
LABEL org.opencontainers.image.source="https://github.com/ahimsalabs/kiki-images"
LABEL org.opencontainers.image.description="kiki's agent image: Ubuntu 26.04, systemd, and the tools agents use"
WORKDIR /home/exedev
# systemd as PID 1: kiki boots an image whose command is systemd in its
# systemd mode, and mounts cgroup2 and the rest itself.
CMD ["/sbin/init"]
