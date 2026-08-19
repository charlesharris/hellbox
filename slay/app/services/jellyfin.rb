require "net/http"
require "uri"

# Tells Jellyfin to look again.
#
# v1's README carried "nothing tells Jellyfin to rescan" as an outstanding item
# for its whole life, and the cost of fixing it turns out to be one POST. A
# library that appears when the disc is done, rather than whenever Jellyfin
# next happens to sweep, is most of what filing is for.
#
# Unconfigured is a normal state, not an error: Jellyfin has its own stack and
# its own lifetime here, and a filing run must never fail because a media
# server on another port was restarting. Everything below either works or is
# logged.
class Jellyfin
  # Jellyfin's own header name, kept from Emby.
  TOKEN_HEADER = "X-Emby-Token".freeze

  def self.notify = new.notify

  def initialize(url: ENV["JELLYFIN_URL"], key: ENV["JELLYFIN_API_KEY"])
    @url = url.presence
    @key = key.presence
  end

  def configured? = @url.present? && @key.present?

  # A full library refresh rather than a targeted one. Jellyfin's per-path
  # endpoints want its own internal ids, which would mean holding a second
  # mapping of the library in this database purely to avoid a scan that takes
  # seconds on a collection this size.
  def notify
    unless configured?
      Rails.logger.info("Jellyfin: no JELLYFIN_URL/JELLYFIN_API_KEY set — not asking for a rescan")
      return false
    end

    uri = URI.join(@url.chomp("/") + "/", "Library/Refresh")
    request = Net::HTTP::Post.new(uri)
    request[TOKEN_HEADER] = @key
    request["Content-Length"] = "0"

    response = Net::HTTP.start(uri.host, uri.port, use_ssl: uri.scheme == "https",
                                                   open_timeout: 3, read_timeout: 5) do |http|
      http.request(request)
    end

    unless response.is_a?(Net::HTTPSuccess)
      Rails.logger.warn("Jellyfin: rescan refused (#{response.code})")
      return false
    end

    Rails.logger.info("Jellyfin: rescan requested")
    true
  rescue StandardError => e
    # The files are filed. Jellyfin finding them late is a delay; raising here
    # would make it look like the filing itself failed.
    Rails.logger.warn("Jellyfin: could not ask for a rescan (#{e.class}: #{e.message})")
    false
  end
end
