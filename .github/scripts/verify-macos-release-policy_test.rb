#!/usr/bin/env ruby

require "minitest/autorun"
require "open3"
require "tempfile"
require "yaml"

class VerifyMacosReleasePolicyTest < Minitest::Test
  ROOT = File.expand_path("../..", __dir__)
  VERIFIER = File.join(__dir__, "verify-macos-release-policy.rb")
  CONFIG = YAML.safe_load(File.read(File.join(ROOT, ".goreleaser.yaml")), permitted_classes: [], aliases: false)

  def verify(config)
    Tempfile.create(["goreleaser", ".yaml"]) do |file|
      file.write(YAML.dump(config))
      file.flush
      _stdout, stderr, status = Open3.capture3("ruby", VERIFIER, file.path)
      return [status.success?, stderr]
    end
  end

  def mutate
    config = Marshal.load(Marshal.dump(CONFIG))
    yield config
    config
  end

  def test_current_configuration_passes
    success, stderr = verify(CONFIG)

    assert success, stderr
  end

  def test_rejects_an_additional_unsigned_darwin_build
    config = mutate do |value|
      value["builds"] << {
        "id" => "darwin-helper",
        "main" => "./cmd/trello-cli",
        "env" => ["CGO_ENABLED=0"],
        "goos" => ["darwin"],
        "goarch" => ["arm64"],
      }
    end

    success, stderr = verify(config)

    refute success
    assert_includes stderr, "Darwin build darwin-helper does not enable cgo"
    assert_includes stderr, "enabled macOS notarization for darwin-helper is missing"
  end

  def test_rejects_darwin_added_to_the_portable_build
    config = mutate do |value|
      portable = value["builds"].find { |build| build["id"] == "trello-cli-portable" }
      portable["goos"] << "darwin"
    end

    success, stderr = verify(config)

    refute success
    assert_includes stderr, "Darwin build trello-cli-portable does not enable cgo"
    assert_includes stderr, "enabled macOS notarization for trello-cli-portable is missing"
  end

  def test_rejects_the_named_build_moving_off_darwin
    config = mutate do |value|
      darwin = value["builds"].find { |build| build["id"] == "trello-cli-darwin" }
      darwin["goos"] = ["linux"]
    end

    success, stderr = verify(config)

    refute success
    assert_includes stderr, "build trello-cli-darwin must target only Darwin"
    assert_includes stderr, "no Darwin build is configured"
  end

  def test_rejects_a_missing_darwin_architecture
    config = mutate do |value|
      darwin = value["builds"].find { |build| build["id"] == "trello-cli-darwin" }
      darwin["goarch"] = ["arm64"]
    end

    success, stderr = verify(config)

    refute success
    assert_includes stderr, "build trello-cli-darwin must target amd64 and arm64"
  end

  def test_rejects_a_darwin_target_that_overrides_goos
    config = mutate do |value|
      value["builds"] << {
        "id" => "targeted-helper",
        "main" => "./cmd/trello-cli",
        "env" => ["CGO_ENABLED=0"],
        "goos" => ["linux"],
        "goarch" => ["arm64"],
        "targets" => ["darwin_arm64"],
      }
    end

    success, stderr = verify(config)

    refute success
    assert_includes stderr, "build targeted-helper must not override the platform matrix with targets"
  end

  def test_rejects_universal_binaries
    config = mutate do |value|
      value["universal_binaries"] = [{ "id" => "trello-cli-universal", "ids" => ["trello-cli-darwin"] }]
    end

    success, stderr = verify(config)

    refute success
    assert_includes stderr, "universal binaries are not permitted by the macOS release policy"
  end

  def test_rejects_notarization_that_does_not_wait
    config = mutate do |value|
      value.dig("notarize", "macos", 0, "notarize")["wait"] = false
    end

    success, stderr = verify(config)

    refute success
    assert_includes stderr, "notarize.wait must be true"
  end

  def test_rejects_a_quarantine_bypass
    config = mutate do |value|
      value["homebrew_casks"][0]["hooks"] = {
        "post" => { "install" => "/usr/bin/xattr -dr com.apple.quarantine trello-cli" },
      }
    end

    success, stderr = verify(config)

    refute success
    assert_includes stderr, "configuration contains a Gatekeeper quarantine bypass"
  end
end
