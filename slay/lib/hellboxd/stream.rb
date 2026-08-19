require "net/http"
require "uri"
require "json"

module Hellboxd
  # Consumes the daemon's Server-Sent Events stream.
  #
  # SSE rather than polling because the daemon already speaks it, and because
  # reconnection is in the protocol: the cursor goes out as Last-Event-ID and
  # the daemon replays from there.
  #
  # The daemon can also say it *cannot* replay — when a client has been away
  # longer than its ring buffer holds. That arrives as a "reconcile" event and
  # must not be ignored: silently continuing would leave a gap in the catalog
  # that nothing would ever detect.
  class Stream
    def initialize(base_url: ENV.fetch("HELLBOXD_URL", "http://127.0.0.1:9494"), logger: nil)
      @base = URI(base_url)
      @logger = logger
    end

    # each yields [kind, event_hash] until the connection drops.
    def each(from_id: 0)
      uri = URI.join(@base.to_s.chomp("/") + "/", "v1/events")
      req = Net::HTTP::Get.new(uri)
      req["Accept"] = "text/event-stream"
      req["Last-Event-ID"] = from_id.to_s if from_id.to_i.positive?

      Net::HTTP.start(uri.host, uri.port, read_timeout: nil) do |http|
        http.request(req) do |res|
          raise "hellboxd answered #{res.code}" unless res.code == "200"

          buffer = +""
          res.read_body do |chunk|
            buffer << chunk
            # Frames are separated by a blank line. Anything after the last one
            # is a partial frame and stays in the buffer.
            while (i = buffer.index("\n\n"))
              frame = buffer.slice!(0..i + 1)
              parsed = parse(frame)
              yield parsed if parsed
            end
          end
        end
      end
    end

    private

    def parse(frame)
      kind = nil
      id = nil
      data = +""
      frame.each_line do |line|
        line = line.chomp
        next if line.empty? || line.start_with?(":") # ":" is a keepalive
        key, _, value = line.partition(": ")
        case key
        when "event" then kind = value
        when "id"    then id = value
        when "data"  then data << value
        end
      end
      return nil if kind.nil?

      body = begin
        JSON.parse(data)
      rescue JSON::ParserError
        {}
      end
      { "kind" => kind, "id" => id, "body" => body }
    end
  end
end
