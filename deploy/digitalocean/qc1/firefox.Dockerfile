# The restore drill's Firefox runtime (qc1/firefox-runtime.sh builds it inside the
# disposable runner, from a context holding only web/package.json and
# web/package-lock.json). Playwright's Firefox is a glibc build and cannot start on
# the runner itself (Alpine, musl: spawn fails with ENOENT because there is no
# /lib64/ld-linux-x86-64.so.2), so the proof runs in this Debian container instead.
# Playwright and its Firefox come from the repository's lock file, so the browser is
# the one this Playwright version drives; `install --with-deps` adds the system
# libraries (apt) for it. No application code is in the image.
ARG NODE_IMAGE
FROM ${NODE_IMAGE}
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --omit=optional --no-audit --no-fund --loglevel=error \
 && node node_modules/playwright/cli.js install --with-deps firefox \
 && rm -rf /var/lib/apt/lists/*
