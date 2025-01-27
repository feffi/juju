#!/bin/sh -e
[ -n "${GOPATH:-}" ] && export "PATH=${GOPATH}/bin:${PATH}"

# Always ignore SC2230 ('which' is non-standard. Use builtin 'command -v' instead.)
export SHELLCHECK_OPTS="-e SC2230 -e SC2039 -e SC2028 -e SC2002 -e SC2005"

OPTIND=1
VERBOSE=1
SKIP_LIST=""
ARITFACT_FILE=""
OUTPUT_FILE=""

import_subdir_files() {
    test "$1"
    local file
    for file in "$1"/*.sh; do
        # shellcheck disable=SC1090
        . "$file"
    done
}

import_subdir_files includes

# If adding a test suite, then ensure to add it here to be picked up!
TEST_NAMES="test_static_analysis \
            test_smoke \
            test_cmr_bundles"

show_help() {
    echo ""
    echo "$(red 'Juju test suite')"
    echo "¯¯¯¯¯¯¯¯¯¯¯¯¯¯¯"
    echo "Juju tests suite expects you to have a Juju available on your \$PATH,"
    echo "so that if a tests needs to bootstrap it can just use that one"
    echo "directly."
    echo ""
    echo "Usage:"
    echo "¯¯¯¯¯¯"
    echo "cmd [-h] [-vV] [-s test] [-a file] [-x file] [-r reuse] [-p provider type <lxd|aws>]"
    echo ""
    echo "    $(green 'cmd -h')        Display this help message"
    echo "    $(green 'cmd -v')        Verbose and debug messages"
    echo "    $(green 'cmd -V')        Very verbose and debug messages"
    echo "    $(green 'cmd -s')        Skip tests using a comma seperated list"
    echo "    $(green 'cmd -a')        Create an atifact file"
    echo "    $(green 'cmd -x')        Output file from streaming the output"
    echo "    $(green 'cmd -r')        Reuse bootstrapped controller"
    echo "    $(green 'cmd -p')        Bootstrap provider to use when bootstrapping <lxd|aws>"
    echo ""
    echo "Tests:"
    echo "¯¯¯¯¯¯"
    echo "Available tests:"
    echo ""

    # Let's use the TEST_NAMES to print out what's available
    output=""
    for test in ${TEST_NAMES}; do
        name=$(echo "${test}" | sed -E "s/^run_//g" | sed -E "s/_/ /g")
        # shellcheck disable=SC2086
        output="${output}\n    $(green ${test})|Runs the ${name}"
    done
    echo "${output}" | column -t -s "|"

    echo ""
    echo "Examples:"
    echo "¯¯¯¯¯¯¯¯¯"
    echo "Run a singular test:"
    echo ""
    echo "    $(green 'cmd static_analysis test_static_analysis_go')"
    echo ""
    echo "Run static analysis tests, but skip the go static analysis tests:"
    echo ""
    echo "    $(green 'cmd -s test_static_analysis_go static_analysis')"
    echo ""
    echo "Run a more verbose output and save that to an artifact tar (it"
    echo "requires piping the output from stdout and stderr into a output.log,"
    echo "which is then copied into the artifact tar file on test cleanup):"
    echo ""
    echo "    $(green 'cmd -V -a artifact.tar.gz -x output.log 2>&1|tee output.log')"
    exit 0
}

while getopts "h?:vVsax" opt; do
    case "${opt}" in
    h|\?)
        show_help
        ;;
    v)
        VERBOSE=1
        shift
        ;;
    V)
        VERBOSE=2
        shift
        ;;
    s)
        SKIP_LIST="${2}"
        shift 2
        ;;
    a)
        ARITFACT_FILE="${2}"
        shift 2
        ;;
    x)
        OUTPUT_FILE="${2}"
        shift 2
        ;;
    r)
        export BOOTSTRAP_REUSE="true"
        shift
        ;;
    p)
        export BOOTSTRAP_PROVIDER="${2}"
        shift 2
        ;;
    *)
        echo "Unexpected argument ${opt}" >&2
        exit 1
    esac
done

shift $((OPTIND-1))

[ "${1:-}" = "--" ] && shift

export VERBOSE="${VERBOSE}"
export SKIP_LIST="${SKIP_LIST}"

echo ""

echo "==> Checking for dependencies"
check_dependencies curl jq shellcheck juju

if [ "${USER:-'root'}" = "root" ]; then
    echo "The testsuite must not be run as root." >&2
    exit 1
fi

cleanup() {
    # Allow for failures and stop tracing everything
    set +ex

    # Allow for inspection
    if [ -n "${TEST_INSPECT:-}" ]; then
        if [ "${TEST_RESULT}" != "success" ]; then
            echo "==> TEST DONE: ${TEST_CURRENT_DESCRIPTION}"
        fi
        echo "==> Test result: ${TEST_RESULT}"
        echo "Tests Completed (${TEST_RESULT}): hit enter to continue"

        # shellcheck disable=SC2034
        read -r nothing
    fi

    echo "==> Cleaning up"

    cleanup_jujus

    echo ""
    if [ "${TEST_RESULT}" != "success" ]; then
        echo "==> TESTS DONE: ${TEST_CURRENT_DESCRIPTION}"
    fi
    echo "==> Test result: ${TEST_RESULT}"

    # Move any artifacts to the choosen location
    if [ -n "${ARITFACT_FILE}" ]; then
        echo "==> Test artifact: ${ARITFACT_FILE}"
        if [ -f "${OUTPUT_FILE}" ]; then
            cp "${OUTPUT_FILE}" "${TEST_DIR}"
        fi
        TAR_OUTPUT=$(tar -C "${TEST_DIR}" --transform s/./artifacts/ -zcvf "${ARITFACT_FILE}" ./ 2>&1)
        # shellcheck disable=SC2181
        if [ $? -ne 0 ]; then
            echo "${TAR_OUTPUT}"
            exit 1
        fi
    fi

    if [ "${TEST_RESULT}" = "success" ]; then
        rm -rf "${TEST_DIR}"
        echo "==> Tests Removed: ${TEST_DIR}"
    fi

    echo "==> TEST COMPLETE"
}

export BOOTSTRAP_PROVIDER="lxd"

TEST_CURRENT=setup
TEST_RESULT=failure

trap cleanup EXIT HUP INT TERM

# Setup test directory
TEST_DIR=$(mktemp -d tmp.XXX | xargs -I % echo "$(pwd)/%")

run_test() {
    TEST_CURRENT=${1}
    TEST_CURRENT_DESCRIPTION=${2:-${1}}
    TEST_CURRENT_NAME=${TEST_CURRENT#"test_"}

    if [ -n "${4}" ]; then
        TEST_CURRENT=${4}
    fi

    import_subdir_files "suites/${TEST_CURRENT_NAME}"

    echo "==> TEST BEGIN: ${TEST_CURRENT_DESCRIPTION}"
    START_TIME=$(date +%s)
    ${TEST_CURRENT}
    END_TIME=$(date +%s)

    echo "==> TEST DONE: ${TEST_CURRENT_DESCRIPTION} ($((END_TIME-START_TIME))s)"
}

# allow for running a specific set of tests
if [ "$#" -gt 0 ]; then
    run_test "test_${1}" "" "$@"
    TEST_RESULT=success
    exit
fi

for test in ${TEST_NAMES}; do
    name=$(echo "${test}" | sed -E "s/^run_//g" | sed -E "s/_/ /g")
    run_test "${test}" "${name}"
done

TEST_RESULT=success
