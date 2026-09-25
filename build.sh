#!/usr/bin/env bash

set -euo pipefail
trap 'echo "An error has occurred! Aborting the script execution..." >&2' ERR

mkdir -p dist/

rm -f dist/*

package=`grep module go.mod | cut -d " " -f 2`
package_split=(${package//\// })
package_name=${package_split[-1]}

main_package=$package/cmd/$package_name

platforms=("windows/amd64" "windows/386" "windows/arm64" "linux/amd64" "linux/386" "linux/arm64" "darwin/amd64" "darwin/arm64")

for platform in "${platforms[@]}"
do
    platform_split=(${platform//\// })
    GOOS=${platform_split[0]}
    GOARCH=${platform_split[1]}
    output_name=$package_name
    archive_name=$package_name'-'$GOOS'-'$GOARCH
    if [ $GOOS = "windows" ]; then
        output_name+='.exe'
        archive_name+='.zip'
    else
        archive_name+='.tar.gz'
    fi

    echo "CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -o ""dist/$output_name"" $main_package"
    CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -o "dist/$output_name" $main_package
    echo "go run tools/archive.go ""dist/$archive_name"" ""dist/$output_name"" README.md LICENSE"
    go run tools/archive.go "dist/$archive_name" "dist/$output_name" README.md LICENSE
    rm "dist/$output_name"

    echo ""
done
