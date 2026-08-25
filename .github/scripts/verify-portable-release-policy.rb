#!/usr/bin/env ruby

require "yaml"

path = ARGV.fetch(0) do
  warn "usage: verify-portable-release-policy.rb PATH"
  exit 2
end

begin
  config = YAML.safe_load(File.read(path), permitted_classes: [], aliases: false)
rescue Errno::ENOENT, Psych::Exception => error
  warn "portable release policy: cannot read #{path}: #{error.message}"
  exit 1
end

unless config.is_a?(Hash)
  warn "portable release policy: #{path} is not a mapping"
  exit 1
end

errors = []

allowed_top_level = %w[version project_name before builds archives checksum snapshot changelog release]
(config.keys - allowed_top_level).sort.each do |section|
  errors << "unsupported top-level section #{section} is not permitted"
end

unless config["before"] == { "hooks" => ["go mod download"] }
  errors << "before hooks must contain only go mod download"
end

builds = Array(config["builds"])
if builds.length != 1
  errors << "exactly one build is required"
else
  build = builds.first
  allowed_build_keys = %w[id main binary env flags ldflags mod_timestamp goos goarch]
  (build.keys - allowed_build_keys).sort.each do |key|
    errors << "unsupported build key #{key} is not permitted"
  end
  errors << "build id must be trello-cli" unless build["id"] == "trello-cli"
  errors << "build must use the Go builder" unless [nil, "go"].include?(build["builder"])
  errors << "build must target only Linux and Windows" unless Array(build["goos"]).sort == %w[linux windows]
  errors << "build must target amd64 and arm64" unless Array(build["goarch"]).sort == %w[amd64 arm64]
  errors << "build must disable cgo" unless Array(build["env"]).include?("CGO_ENABLED=0")
  errors << "build must not override the platform matrix with targets" unless Array(build["targets"]).empty?

  version_flag = "-X github.com/abigotado/trello-cli/internal/cli.releaseVersion=v{{ .Version }}"
  unless Array(build["ldflags"]).any? { |flags| flags.include?(version_flag) }
    errors << "build must inject the releaseVersion fallback"
  end
end

archives = Array(config["archives"])
unless archives.length == 1
  errors << "exactly one trello-cli archive definition is required"
else
  archive = archives.first
  allowed_archive_keys = %w[id ids formats format_overrides name_template files]
  (archive.keys - allowed_archive_keys).sort.each do |key|
    errors << "unsupported archive key #{key} is not permitted"
  end
  errors << "archive must contain only trello-cli" unless Array(archive["ids"]) == ["trello-cli"]
  unless Array(archive["files"]) == ["LICENSE", "README.md", "docs/*"]
    errors << "archive files must contain only the documented release files"
  end
end

release = config["release"]
unless release.is_a?(Hash)
  errors << "release configuration must be a mapping"
else
  allowed_release_keys = %w[draft prerelease replace_existing_artifacts replace_existing_draft footer]
  (release.keys - allowed_release_keys).sort.each do |key|
    errors << "unsupported release key #{key} is not permitted"
  end
end

strings = lambda do |value|
  case value
  when Hash
    value.each_value.flat_map { |item| strings.call(item) }
  when Array
    value.flat_map { |item| strings.call(item) }
  when String
    [value]
  else
    []
  end
end

if strings.call(config).any? { |value| value.match?(/(?:\bxattr\b|com\.apple\.quarantine)/i) }
  errors << "configuration contains a Gatekeeper quarantine bypass"
end

if errors.empty?
  puts "portable release policy ok"
  exit 0
end

errors.each { |error| warn "portable release policy: #{error}" }
exit 1
