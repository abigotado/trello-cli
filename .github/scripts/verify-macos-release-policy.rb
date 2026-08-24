#!/usr/bin/env ruby

require "yaml"

path = ARGV.fetch(0) do
  warn "usage: verify-macos-release-policy.rb PATH"
  exit 2
end

begin
  config = YAML.safe_load(File.read(path), permitted_classes: [], aliases: false)
rescue Errno::ENOENT, Psych::Exception => error
  warn "macOS release policy: cannot read #{path}: #{error.message}"
  exit 1
end

unless config.is_a?(Hash)
  warn "macOS release policy: #{path} is not a mapping"
  exit 1
end

errors = []

builds = Array(config["builds"])
errors << "build trello-cli-darwin is missing" unless builds.any? { |build| build["id"] == "trello-cli-darwin" }
errors << "universal binaries are not permitted by the macOS release policy" unless Array(config["universal_binaries"]).empty?

builds.each do |build|
  id = build["id"] || "<unnamed>"
  errors << "build #{id} must declare goos explicitly" unless build.key?("goos")
  errors << "build #{id} must use the Go builder" unless [nil, "go"].include?(build["builder"])
  errors << "build #{id} must not override the platform matrix with targets" unless Array(build["targets"]).empty?
end

darwin_builds = builds.select { |build| Array(build["goos"]).include?("darwin") }
errors << "no Darwin build is configured" if darwin_builds.empty?

expected = {
  ["sign", "certificate"] => "env:MACOS_SIGN_P12",
  ["sign", "password"] => "{{ .Env.MACOS_SIGN_PASSWORD }}",
  ["notarize", "issuer_id"] => "{{ .Env.MACOS_NOTARY_ISSUER_ID }}",
  ["notarize", "key_id"] => "{{ .Env.MACOS_NOTARY_KEY_ID }}",
  ["notarize", "key"] => "env:MACOS_NOTARY_KEY",
  ["notarize", "wait"] => true,
  ["notarize", "timeout"] => "20m",
}

darwin_builds.each do |build|
  id = build["id"] || "<unnamed>"
  errors << "Darwin build #{id} does not enable cgo" unless Array(build["env"]).include?("CGO_ENABLED=1")

  macos_notary = Array(config.dig("notarize", "macos")).find do |entry|
    entry["enabled"] == true && Array(entry["ids"]).include?(id)
  end
  if macos_notary.nil?
    errors << "enabled macOS notarization for #{id} is missing"
    next
  end

  expected.each do |keys, value|
    actual = keys.reduce(macos_notary) { |node, key| node.is_a?(Hash) ? node[key] : nil }
    errors << "macOS notarization for #{id} #{keys.join('.')} must be #{value.inspect}" unless actual == value
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
  puts "macOS release policy ok"
  exit 0
end

errors.each { |error| warn "macOS release policy: #{error}" }
exit 1
