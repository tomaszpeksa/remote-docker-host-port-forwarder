#!/bin/bash
# Pre-commit hook script for running fast unit tests
# This runs on staged Go files to prevent broken code from being committed

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}Running pre-commit checks...${NC}"

# Get list of staged Go files
STAGED_GO_FILES=$(git diff --cached --name-only --diff-filter=ACM | grep '\.go$' || true)

if [ -z "$STAGED_GO_FILES" ]; then
    echo -e "${YELLOW}No Go files staged, skipping Go checks${NC}"
    exit 0
fi

echo -e "${GREEN}Checking staged Go files...${NC}"

# Check formatting
echo -e "${GREEN}1. Checking code formatting...${NC}"
if [ -n "$(gofmt -s -l $STAGED_GO_FILES)" ]; then
    echo -e "${RED}✗ The following files are not formatted:${NC}"
    gofmt -s -l $STAGED_GO_FILES
    echo -e "${YELLOW}Run 'make fmt' or 'gofmt -s -w .' to fix${NC}"
    exit 1
fi
echo -e "${GREEN}  ✓ Code formatting OK${NC}"

# Run go vet on packages containing staged files
echo -e "${GREEN}2. Running go vet...${NC}"
PACKAGES=$(echo "$STAGED_GO_FILES" | xargs -n1 dirname | sort -u | sed 's|^|./|' | sed 's|$|/...|')
for pkg in $PACKAGES; do
    if ! go vet "$pkg" 2>&1; then
        echo -e "${RED}✗ go vet failed${NC}"
        exit 1
    fi
done
echo -e "${GREEN}  ✓ go vet passed${NC}"

# Run fast unit tests only (exclude integration tests)
echo -e "${GREEN}3. Running unit tests...${NC}"
TEST_PACKAGES=$(echo "$STAGED_GO_FILES" | grep '_test\.go$' | xargs -n1 dirname | sort -u | sed 's|^|./|' || true)

if [ -n "$TEST_PACKAGES" ]; then
    for pkg in $TEST_PACKAGES; do
        # Skip integration tests
        if [[ "$pkg" == *"/integration"* ]]; then
            echo -e "${YELLOW}  ⊘ Skipping integration tests${NC}"
            continue
        fi
        
        echo -e "  Testing: $pkg"
        if ! go test -short -timeout=30s "$pkg" 2>&1; then
            echo -e "${RED}✗ Tests failed in $pkg${NC}"
            exit 1
        fi
    done
    echo -e "${GREEN}  ✓ Unit tests passed${NC}"
else
    echo -e "${YELLOW}  ⊘ No test files staged${NC}"
fi

# Check for common mistakes
echo -e "${GREEN}4. Checking for common mistakes...${NC}"

# Check for debug prints
if echo "$STAGED_GO_FILES" | xargs grep -n 'fmt\.Println\|log\.Println.*DEBUG' 2>/dev/null; then
    echo -e "${YELLOW}⚠ Warning: Debug print statements found (non-blocking)${NC}"
fi

# Check for TODO/FIXME
if echo "$STAGED_GO_FILES" | xargs grep -n 'TODO\|FIXME' 2>/dev/null; then
    echo -e "${YELLOW}⚠ Warning: TODO/FIXME markers found (non-blocking)${NC}"
fi

# Check for hardcoded credentials (basic check)
if echo "$STAGED_GO_FILES" | xargs grep -nE '(password|secret|token|api[_-]?key)\s*[:=]\s*["\x27]' 2>/dev/null; then
    echo -e "${RED}✗ Possible hardcoded credentials detected!${NC}"
    echo -e "${RED}  Please remove sensitive data before committing${NC}"
    exit 1
fi

echo -e "${GREEN}  ✓ Common mistake checks passed${NC}"

echo -e "${GREEN}✓ All pre-commit checks passed!${NC}"
exit 0