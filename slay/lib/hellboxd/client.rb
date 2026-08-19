require "net/http"
require "uri"
require "json"

# Talks to hellboxd.
#
# The daemon owns the hardware and every fact about it. This asks; it never
# assumes. In particular it does not cache drive state, because a drive's state
# is the one thing on the dashboard that is worthless if it is even slightly
# stale.
module Hellboxd
  class Error < StandardError; end
  class Unreachable < Error; end

  class Client
    # The protocol version this client understands. The daemon carries its own
    # in every response; a mismatch is worth refusing rather than misreading.
    EXPECTED_VERSION = 2

    def initialize(base_url: ENV.fetch("HELLBOXD_URL", "http://127.0.0.1:9494"), timeout: 5)
      @base = URI(base_url)
      @timeout = timeout
    end

    def drives  = get("/v1/drives").fetch("data", [])
    def health  = get("/v1/health").fetch("data", [])
    def disc(fingerprint) = get("/v1/discs/#{fingerprint}").fetch("data", nil)

    def eject(drive)  = post("/v1/drives/#{drive}/eject")
    def cancel(drive) = post("/v1/drives/#{drive}/cancel")
    def rescan        = post("/v1/rescan")
    def forget(fp)    = post("/v1/discs/#{fp}/forget")

    # Whether the daemon is answering at all. The dashboard has to distinguish
    # "no drives" from "the daemon is down", because they look identical in an
    # empty list and mean completely different things.
    def up?
      health
      true
    rescue Error
      false
    end

    private

    def get(path)  = request(Net::HTTP::Get.new(uri(path)))
    def post(path) = request(Net::HTTP::Post.new(uri(path)))

    def uri(path)
      URI.join(@base.to_s.chomp("/") + "/", path.delete_prefix("/"))
    end

    def request(req)
      res = Net::HTTP.start(@base.host, @base.port, read_timeout: @timeout, open_timeout: @timeout) do |http|
        http.request(req)
      end
      parse(res)
    rescue Errno::ECONNREFUSED, Errno::EHOSTUNREACH, SocketError, Net::OpenTimeout, Net::ReadTimeout => e
      raise Unreachable, "hellboxd is not answering at #{@base}: #{e.class}"
    end

    def parse(res)
      body = JSON.parse(res.body.to_s)
      if body["version"] && body["version"] != EXPECTED_VERSION
        raise Error, "hellboxd speaks version #{body["version"]}, this client speaks #{EXPECTED_VERSION}"
      end
      raise Error, body["error"].presence || "hellboxd returned #{res.code}" unless body["ok"]

      body
    rescue JSON::ParserError
      raise Error, "hellboxd returned something that is not JSON (#{res.code})"
    end
  end
end
