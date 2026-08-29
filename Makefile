GO      ?= go
BIN     := bin
PKG     := ./...
BIGROWS ?= 5000000

# Every binary embeds the frontend and links the whole tree, so all of them
# depend on all of it. Listing this once and using it everywhere is what stops
# a target from quietly serving a stale build.
SRC := $(shell find cmd internal -type f \( -name '*.go' -o -name '*.css' -o -name '*.js' \) 2>/dev/null)

.PHONY: all
all: $(BIN)/sqldoc $(BIN)/sqldoc-view

$(BIN)/sqldoc: $(SRC) go.mod
	@mkdir -p $(BIN)
	$(GO) build -o $@ ./cmd/sqldoc

# The native window hosts the system WebView, so it always needs cgo; it is
# built against the cgo SQLite driver for consistency.
$(BIN)/sqldoc-view: $(SRC) go.mod
	@mkdir -p $(BIN)
	$(GO) build -tags cgosqlite -o $@ ./cmd/sqldoc-view

.PHONY: cgo
cgo: $(SRC)
	@mkdir -p $(BIN)
	$(GO) build -tags cgosqlite -o $(BIN)/sqldoc-cgo ./cmd/sqldoc

.PHONY: test
test:
	$(GO) test $(PKG)

.PHONY: race
race:
	$(GO) test -race $(PKG)

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: check
check: vet test

# Prove the pure-Go build really is pure: if this fails, a cgo dependency has
# crept into the default build and it can no longer be cross-compiled.
.PHONY: crosscheck
crosscheck:
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 $(GO) build -o /dev/null ./cmd/sqldoc
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -o /dev/null ./cmd/sqldoc
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 $(GO) build -o /dev/null ./cmd/sqldoc
	@echo "pure Go build cross-compiles cleanly"

.PHONY: testdata
testdata: testdata/big.db testdata/demo.db

testdata/big.db:
	@mkdir -p testdata
	@echo "building a $(BIGROWS)-row database..."
	@sqlite3 $@ "PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF; \
	  CREATE TABLE events(id INTEGER PRIMARY KEY, ts TEXT, user TEXT, action TEXT, \
	    region TEXT, latency_ms REAL, bytes INTEGER, ok INTEGER, note TEXT); \
	  INSERT INTO events(ts,user,action,region,latency_ms,bytes,ok,note) \
	  SELECT datetime(1735689600 + n*3, 'unixepoch'), 'user_' || (n % 50000), \
	    (CASE n%6 WHEN 0 THEN 'login' WHEN 1 THEN 'view' WHEN 2 THEN 'purchase' \
	              WHEN 3 THEN 'search' WHEN 4 THEN 'logout' ELSE 'refund' END), \
	    (CASE n%5 WHEN 0 THEN 'us-east' WHEN 1 THEN 'us-west' WHEN 2 THEN 'eu-central' \
	              WHEN 3 THEN 'ap-south' ELSE 'sa-east' END), \
	    round((n%997)/7.0, 3), (n%65536)*17, (n%37!=0), \
	    'session ' || hex(randomblob(6)) || ' trace-' || n \
	  FROM (WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n < $(BIGROWS)) SELECT n FROM c);"

testdata/demo.db: testdata/demo.sql
	@sqlite3 $@ < $<

.PHONY: bench
bench: $(BIN)/sqldoc testdata/big.db
	$(BIN)/sqldoc bench testdata/big.db

.PHONY: bench-all
bench-all: $(BIN)/sqldoc cgo testdata/big.db
	@echo "--- pure Go ---"; $(BIN)/sqldoc bench testdata/big.db
	@echo "--- cgo ---";     $(BIN)/sqldoc-cgo bench testdata/big.db

.PHONY: clean
clean:
	rm -rf $(BIN)

.PHONY: distclean
distclean: clean
	rm -f testdata/big.db testdata/demo.db

# ---------------------------------------------------------------- packaging
APP     := $(BIN)/sqldoc.app
APPDEST ?= /Applications

# A .app bundle is what makes a database double-clickable: Launch Services
# reads the document types out of Info.plist and passes the file's path as
# argv[1], which is exactly what sqldoc-view already expects.
$(BIN)/sqldoc-launcher: packaging/macos/Launcher.swift
	@mkdir -p $(BIN)
	swiftc -O -o $@ $<

# The bundle contains two applications. The outer one is what Finder launches
# and what receives the "open documents" Apple Event; the nested one is the
# viewer, which has to be a bundle in its own right or Launch Services will not
# give it a session that can put a window on screen.
VIEWERAPP := $(APP)/Contents/Library/sqldoc.app

# The icon goes into both bundles. Finder reads the outer one; the Dock tile
# belongs to the viewer, because that is the process that owns the window.
ICON := packaging/macos/sqldoc.icns

.PHONY: app
app: $(BIN)/sqldoc-view $(BIN)/sqldoc-launcher packaging/macos/Info.plist packaging/macos/Viewer-Info.plist $(ICON)
	@rm -rf $(APP)
	@mkdir -p $(APP)/Contents/MacOS $(APP)/Contents/Resources
	@mkdir -p $(VIEWERAPP)/Contents/MacOS $(VIEWERAPP)/Contents/Resources
	@cp packaging/macos/Info.plist $(APP)/Contents/Info.plist
	@cp $(BIN)/sqldoc-launcher $(APP)/Contents/MacOS/sqldoc-launcher
	@cp $(ICON) $(APP)/Contents/Resources/sqldoc.icns
	@printf 'APPL????' > $(APP)/Contents/PkgInfo
	@cp packaging/macos/Viewer-Info.plist $(VIEWERAPP)/Contents/Info.plist
	@cp $(BIN)/sqldoc-view $(VIEWERAPP)/Contents/MacOS/sqldoc-view
	@cp $(ICON) $(VIEWERAPP)/Contents/Resources/sqldoc.icns
	@printf 'APPL????' > $(VIEWERAPP)/Contents/PkgInfo
	@codesign --force --sign - $(VIEWERAPP) 2>/dev/null || true
	@codesign --force --sign - $(APP) 2>/dev/null || \
	  echo "note: ad-hoc signing unavailable; the app still runs locally"
	@echo "built $(APP)"

# Installing registers the bundle with Launch Services, which is what puts
# sqldoc in Finder's Open With menu.
.PHONY: install-app
install-app: app
	@rm -rf "$(APPDEST)/sqldoc.app"
	@cp -R $(APP) "$(APPDEST)/sqldoc.app"
	@/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister \
	  -f "$(APPDEST)/sqldoc.app" 2>/dev/null || true
	@echo "installed to $(APPDEST)/sqldoc.app"
	@echo "right-click any .db in Finder -> Open With -> sqldoc"

.PHONY: install
install: $(BIN)/sqldoc
	@install -d $(HOME)/.local/bin
	@install -m 0755 $(BIN)/sqldoc $(HOME)/.local/bin/sqldoc
	@echo "installed $(HOME)/.local/bin/sqldoc"

# Makes a double-clicked database open in sqldoc. Kept separate from
# install-app because taking over a file type is a decision, not a side effect
# of installing something.
.PHONY: set-default
set-default: $(BIN)/set-default-handler install-app
	@$(BIN)/set-default-handler

$(BIN)/set-default-handler: packaging/macos/set-default-handler.swift
	@mkdir -p $(BIN)
	@swiftc -O -o $@ $< 2>/dev/null || \
	  echo "note: swiftc unavailable; set the default with Finder's Get Info instead"

# Everything, in the right order. Using this rather than running the steps by
# hand is what keeps an installed binary from lagging behind the source.
.PHONY: install-all
install-all: clean all app
	@install -d $(HOME)/.local/bin
	@install -m 0755 $(BIN)/sqldoc      $(HOME)/.local/bin/sqldoc
	@install -m 0755 $(BIN)/sqldoc-view $(HOME)/.local/bin/sqldoc-view
	@rm -rf "$(APPDEST)/sqldoc.app"
	@cp -R $(APP) "$(APPDEST)/sqldoc.app"
	@/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister \
	  -f "$(APPDEST)/sqldoc.app" 2>/dev/null || true
	@echo
	@echo "sqldoc      -> $(HOME)/.local/bin/sqldoc"
	@echo "sqldoc.app  -> $(APPDEST)/sqldoc.app"
	@echo "run 'make set-default' to open databases with a double-click"
