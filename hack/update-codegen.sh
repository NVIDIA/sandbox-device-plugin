#!/bin/sh

# Exit immediately if a command exits with a non-zero status.
set -e

echo "Starting protobuf code generation..."

# Create the output directory if it doesn't exist
OUTPUT_DIR="."

# Define the root directory for all proto files
BASEDIR="$(dirname "$0")"
cd ${BASEDIR}/..

BASE_PATH=`pwd`
PROTO_PATH="./"
cd ./pkg/apis

# Find all .proto files and compile them
# The -I flag specifies the directory to search for imports.
# The --go_out flag specifies the output directory and options for the Go plugin.
find "${PROTO_PATH}" -name "*.proto" | while read PROTO_FILE; do
  echo "Generating code for ${PROTO_FILE}"
  protoc \
    -I="${PROTO_PATH}":"${BASE_PATH}/vendor" \
    --go_out=paths=source_relative:"${OUTPUT_DIR}" \
    --go-grpc_out=paths=source_relative:${OUTPUT_DIR} \
    "${PROTO_FILE}"
done

echo "Protobuf code generation finished."

