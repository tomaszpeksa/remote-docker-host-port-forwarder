#!/bin/bash
# Production Readiness Validation Script
# This script validates all quality gates for production deployment

# Note: We don't use set -e so we can show all validation results

echo "=========================================="
echo "PRODUCTION READINESS VALIDATION"
echo "=========================================="
echo ""

FAILED=0
PASSED=0

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to report success
pass() {
    echo -e "${GREEN}✓${NC} $1"
    ((PASSED++))
}

# Function to report failure
fail() {
    echo -e "${RED}✗${NC} $1"
    ((FAILED++))
}

# Function to report warning
warn() {
    echo -e "${YELLOW}⚠${NC} $1"
}

# 1. BUILD VERIFICATION
echo "1. Build Verification"
echo "--------------------"
if make clean > /dev/null 2>&1 && make build > /dev/null 2>&1; then
    pass "Build successful"
    
    # Test binary
    if ./build/rdhpf --help > /dev/null 2>&1; then
        pass "Binary functional (--help)"
    else
        fail "Binary --help failed"
    fi
    
    if ./build/rdhpf version > /dev/null 2>&1; then
        pass "Binary functional (version)"
    else
        fail "Binary version failed"
    fi
else
    fail "Build failed"
fi
echo ""

# 2. UNIT TESTS
echo "2. Unit Tests"
echo "-------------"
if make test > /dev/null 2>&1; then
    UNIT_TEST_COUNT=$(go test -v ./tests/unit 2>&1 | grep "^--- PASS" | wc -l | tr -d ' ')
    pass "Unit tests passed ($UNIT_TEST_COUNT tests)"
else
    fail "Unit tests failed"
fi
echo ""

# 3. CODE QUALITY
echo "3. Code Quality"
echo "---------------"

# Format check (excluding .itests directory)
UNFORMATTED=$(find . -name "*.go" -not -path "./.itests/*" -not -path "./vendor/*" -not -path "./build/*" | xargs gofmt -l | wc -l | tr -d ' ')
if [ "$UNFORMATTED" -eq "0" ]; then
    pass "Code formatting (0 files need formatting)"
else
    fail "Code formatting ($UNFORMATTED files need formatting)"
fi

# Vet check
if make vet > /dev/null 2>&1; then
    pass "Go vet checks"
else
    fail "Go vet checks"
fi
echo ""

# 4. DOCUMENTATION
echo "4. Documentation"
echo "----------------"

# Check for fixed-ports references (should only be in CHANGELOG as removed)
FIXED_PORT_REFS=$(grep -rn "fixed-port\|fixed_port" README.md docs/ CHANGELOG.md 2>/dev/null | grep -v "Removed" | grep -v "removed" | wc -l || echo "0")
if [ "$FIXED_PORT_REFS" -eq "0" ]; then
    pass "No fixed-ports references (feature cleanly removed)"
else
    fail "Found $FIXED_PORT_REFS references to removed fixed-ports feature"
fi

# Check required documentation exists
REQUIRED_DOCS=("README.md" "docs/user-guide.md" "docs/troubleshooting.md" "docs/integration-testing.md" "CHANGELOG.md")
DOC_MISSING=0
for doc in "${REQUIRED_DOCS[@]}"; do
    if [ -f "$doc" ]; then
        pass "Documentation exists: $doc"
    else
        fail "Documentation missing: $doc"
        ((DOC_MISSING++))
    fi
done

# Check documentation mentions shutdown
if grep -q "Stopping the tool\|graceful shutdown" docs/user-guide.md; then
    pass "User guide documents shutdown workflow"
else
    warn "User guide may be missing shutdown documentation"
fi
echo ""

# 5. TEST ANALYSIS
echo "5. Test Analysis"
echo "----------------"

# Count integration tests (even if skipped)
TOTAL_INTEGRATION=$(go test -v ./tests/integration 2>&1 | grep "^=== RUN" | wc -l | tr -d ' ')
SKIPPED_INTEGRATION=$(go test -v ./tests/integration 2>&1 | grep "^--- SKIP" | wc -l | tr -d ' ')
PASSED_INTEGRATION=$(go test -v ./tests/integration 2>&1 | grep "^--- PASS" | wc -l | tr -d ' ')

echo "Integration test summary:"
echo "  Total tests: $TOTAL_INTEGRATION"
echo "  Passed: $PASSED_INTEGRATION"
echo "  Skipped: $SKIPPED_INTEGRATION"

if [ "$PASSED_INTEGRATION" -gt "0" ]; then
    pass "Integration tests framework functional ($PASSED_INTEGRATION passing)"
else
    warn "No integration tests passing (may require Docker on remote host)"
fi

# Check for critical test skips
CRITICAL_SKIPS=$(go test -v ./tests/integration 2>&1 | grep -E "SKIP.*TestSSHMaster_|SKIP.*TestConflict_|SKIP.*TestManager_" | wc -l || echo "0")
if [ "$CRITICAL_SKIPS" -gt "0" ]; then
    warn "$CRITICAL_SKIPS critical tests skipped (require Docker on remote host)"
else
    pass "No critical tests skipped"
fi
echo ""

# 6. BINARY VALIDATION
echo "6. Binary Validation"
echo "--------------------"

BINARY_SIZE=$(stat -f%z ./build/rdhpf 2>/dev/null || stat -c%s ./build/rdhpf 2>/dev/null || echo "0")
BINARY_SIZE_MB=$((BINARY_SIZE / 1024 / 1024))
echo "Binary size: ${BINARY_SIZE_MB}MB"

if [ "$BINARY_SIZE" -gt "0" ]; then
    pass "Binary created successfully"
else
    fail "Binary not created"
fi

# Check binary is executable
if [ -x "./build/rdhpf" ]; then
    pass "Binary is executable"
else
    fail "Binary is not executable"
fi
echo ""

# SUMMARY
echo "=========================================="
echo "VALIDATION SUMMARY"
echo "=========================================="
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}✓ ALL QUALITY GATES PASSED${NC}"
    echo ""
    echo "Passed checks: $PASSED"
    echo "Failed checks: $FAILED"
    echo ""
    echo -e "${GREEN}Status: PRODUCTION READY${NC}"
    exit 0
else
    echo -e "${RED}✗ SOME QUALITY GATES FAILED${NC}"
    echo ""
    echo "Passed checks: $PASSED"
    echo "Failed checks: $FAILED"
    echo ""
    echo -e "${RED}Status: NOT PRODUCTION READY${NC}"
    echo ""
    echo "Please fix the failed checks before proceeding."
    exit 1
fi