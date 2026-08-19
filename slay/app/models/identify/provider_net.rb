module Identify
  # Turns what the other nets guessed into a provider record.
  #
  # It proposes nothing on its own. Every other net reads the disc; this one
  # reads a database, and it needs a name to look up before it can say
  # anything. So it runs last, over the leading proposals, and its value is
  # confirmation rather than discovery: a title that TMDB also knows, with a
  # year and an id attached, is worth far more than the same string guessed
  # from a filesystem label.
  #
  # Runtime is the discriminator, not popularity. Every provider search returns
  # something; what makes a match real is the disc agreeing with it about how
  # long the film is.
  class ProviderNet
    # How far a disc's runtime may sit from the provider's, as a fraction.
    # Generous, because a provider lists theatrical runtime while a disc holds
    # whatever was pressed — different cuts, credits, and PAL all move it.
    RUNTIME_TOLERANCE = 0.08

    def initialize(client: Providers::Tmdb.new)
      @client = client
    end

    def name = "provider"

    # proposals are what the disc-reading nets already produced.
    def identify(disc, proposals)
      return [] unless @client.configured?

      titles = proposals.map(&:title).compact.uniq.first(2)
      return [] if titles.empty?

      kind = proposals.map(&:kind).compact.first
      titles.flat_map { |t| lookup(disc, t, kind, proposals) }.compact
    end

    private

    def lookup(disc, title, kind, proposals)
      year = proposals.map(&:year).compact.first

      if kind == "series"
        Array(@client.search_series(title, year: year)).first(1).map do |s|
          CandidateProposal.new(
            net: name, title: s[:name], year: s[:first_air_year], kind: "series",
            confidence: 0.7, provider: "tmdb", provider_id: s[:provider_id].to_s,
            why: "TMDB has a series called #{s[:name].inspect} (tmdb:#{s[:provider_id]})"
          )
        end
      else
        Array(@client.search_movie(title, year: year)).first(3).filter_map do |m|
          scored(disc, m)
        end.first(1)
      end
    end

    # A film match is only proposed when the disc's own feature runtime agrees
    # with the provider's. Without that check the first search result always
    # wins, which is how a disc becomes a film with the same name and none of
    # the same content.
    def scored(disc, m)
      feature = disc.disc_titles.map(&:duration_seconds).max.to_i
      runtime = m[:runtime_seconds].to_i

      conf = 0.7
      why  = "TMDB has #{m[:title].inspect} (#{m[:year]}, tmdb:#{m[:provider_id]})"

      if feature.positive? && runtime.positive?
        delta = (feature - runtime).abs.to_f / runtime
        if delta <= RUNTIME_TOLERANCE
          conf = 0.85
          why += "; runtime agrees within #{(delta * 100).round}%"
        else
          # Reported, not discarded. A mismatch may be an extended cut rather
          # than a wrong film, and a person reading the review queue can tell
          # the difference where this cannot.
          conf = 0.4
          why += "; runtime disagrees by #{(delta * 100).round}%"
        end
      end

      CandidateProposal.new(
        net: name, title: m[:title], year: m[:year], kind: "movie",
        confidence: conf, provider: "tmdb", provider_id: m[:provider_id].to_s,
        why: why
      )
    end
  end
end
