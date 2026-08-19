require "net/http"
require "uri"
require "json"

module Providers
  # TMDB, for both films and television.
  #
  # Chosen over TVDB as the first provider because it covers both kinds, and
  # because a personal API key is free where TVDB v4 gates some access behind a
  # paid one. A second provider is additive: nothing here assumes it is alone.
  #
  # With no key configured every method returns nil rather than raising. An
  # unconfigured provider must degrade to "this net had nothing to say", which
  # is an ordinary answer, not a broken installation.
  class Tmdb
    BASE = "https://api.themoviedb.org/3".freeze
    NAME = "tmdb".freeze

    def initialize(api_key: ENV["TMDB_API_KEY"].presence, logger: Rails.logger)
      @key = api_key
      @logger = logger
    end

    def configured? = @key.present?

    # Films matching a title, best first.
    def search_movie(title, year: nil)
      return nil unless configured?
      params = { query: title }
      params[:year] = year if year
      body = get("/search/movie", params, cache_key: "movie:#{title.downcase}:#{year}")
      Array(body&.dig("results")).map { |r| movie_from(r) }
    end

    # Television series matching a title, best first.
    def search_series(title, year: nil)
      return nil unless configured?
      params = { query: title }
      params[:first_air_date_year] = year if year
      body = get("/search/tv", params, cache_key: "tv:#{title.downcase}:#{year}")
      Array(body&.dig("results")).map { |r| series_from(r) }
    end

    # Every episode of a season, in the provider's order.
    #
    # The ordering is recorded rather than assumed. Providers disagree about
    # it, and for some series it genuinely matters — Firefly's broadcast order
    # is not its intended order, and runtime alignment cannot tell two
    # orderings of the same episodes apart.
    def season(series_id, number)
      return nil unless configured?
      body = get("/tv/#{series_id}/season/#{number}", {},
                 cache_key: "season:#{series_id}:#{number}")
      return nil unless body
      Array(body["episodes"]).map do |e|
        {
          number: e["episode_number"],
          name: e["name"],
          runtime_seconds: e["runtime"] ? e["runtime"] * 60 : nil,
          aired_on: e["air_date"],
          ordering: "default"
        }
      end
    end

    private

    def movie_from(r)
      {
        provider: NAME, provider_id: r["id"].to_s, title: r["title"],
        year: r["release_date"].to_s[0, 4].presence&.to_i,
        popularity: r["popularity"].to_f
      }
    end

    def series_from(r)
      {
        provider: NAME, provider_id: r["id"].to_s, name: r["name"],
        first_air_year: r["first_air_date"].to_s[0, 4].presence&.to_i,
        popularity: r["popularity"].to_f
      }
    end

    def get(path, params, cache_key:)
      ProviderCache.fetch(provider: NAME, endpoint: path, key: cache_key) do
        uri = URI("#{BASE}#{path}")
        uri.query = URI.encode_www_form(params.merge(api_key: @key))
        res = Net::HTTP.get_response(uri)
        unless res.is_a?(Net::HTTPSuccess)
          # Not cached: a rate limit or an outage is not an answer, and storing
          # it would make a transient failure permanent.
          @logger&.warn("tmdb #{path}: HTTP #{res.code}")
          next nil
        end
        JSON.parse(res.body)
      end
    rescue StandardError => e
      @logger&.warn("tmdb #{path}: #{e.class}: #{e.message}")
      nil
    end
  end
end
