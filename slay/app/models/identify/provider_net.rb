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
  # long the film is. That has to decide *which* result is proposed and not
  # merely what confidence is written beside it — see #lookup.
  class ProviderNet
    # How far a disc's runtime may sit from the provider's, as a fraction.
    # Generous, because a provider lists theatrical runtime while a disc holds
    # whatever was pressed — different cuts, credits, and PAL all move it.
    RUNTIME_TOLERANCE = 0.08

    # How many results to score before choosing. Position no longer decides
    # anything, so this is only a bound on work: the right film is not
    # somewhere past the fifth result for a title read off the disc itself.
    CANDIDATES = 5

    def initialize(client: Providers::Tmdb.new)
      @client = client
    end

    def name = "provider"

    # proposals are what the disc-reading nets already produced.
    def identify(disc, proposals)
      return [] unless @client.configured?

      # Ranked by confidence rather than taken in the order the nets happen to
      # be registered. The disc's own name is much better evidence than its
      # volume label, and searching on the label first because LabelNet was
      # listed first is an accident, not a decision.
      ranked = Array(proposals).sort_by { |p| -p.confidence.to_f }

      titles = ranked.map(&:title).compact.uniq.first(2)
      return [] if titles.empty?

      kind = ranked.map(&:kind).compact.first
      titles.flat_map { |t| lookup(disc, t, kind, ranked) }.compact
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
        # Choose by score, not by rank. Scoring every result and then taking
        # the first one back from TMDB meant popularity picked the film and the
        # runtime check only annotated it: searching "The Karate Kid" returns
        # the 2010 remake above the 1984 film, so the disc that this project
        # uses as its own worked example proposed the remake at low confidence
        # and never proposed the film it actually holds.
        #
        # max_by keeps the first of any tie, so where nothing distinguishes two
        # results on runtime, TMDB's own ranking still breaks it.
        best = Array(@client.search_movie(title, year: year))
               .first(CANDIDATES)
               .map { |m| scored(disc, m) }
               .max_by(&:confidence)
        best ? [best] : []
      end
    end

    # A film match is only believed when the disc's own feature runtime agrees
    # with the provider's. Without that check the most popular search result
    # always wins, which is how a disc becomes a film with the same name and
    # none of the same content.
    def scored(disc, m)
      feature = disc.disc_titles.map(&:duration_seconds).max.to_i
      runtime = m[:runtime_seconds].to_i

      conf = 0.7
      why  = "TMDB has #{m[:title].inspect} (#{m[:year]}, tmdb:#{m[:provider_id]})"
      rejected = false

      if feature.positive? && runtime.positive?
        delta = (feature - runtime).abs.to_f / runtime
        if delta <= RUNTIME_TOLERANCE
          conf = 0.85
          why += "; runtime agrees within #{(delta * 100).round}%"
        else
          # Reported, not discarded. A mismatch may be an extended cut rather
          # than a wrong film, and a person reading the review queue can tell
          # the difference where this cannot.
          #
          # Marked rejected all the same, so that "reported" cannot quietly
          # become "believed". A demoted match used to keep its seat at the
          # table: the resolver merged it with whatever agreed on the title,
          # counted it as a second net corroborating, and let it hand over the
          # tmdb id and the year that end up in the NFO.
          conf = 0.4
          rejected = true
          why += "; runtime disagrees by #{(delta * 100).round}%"
        end
      end

      CandidateProposal.new(
        net: name, title: m[:title], year: m[:year], kind: "movie",
        confidence: conf, provider: "tmdb", provider_id: m[:provider_id].to_s,
        why: why, rejected: rejected
      )
    end
  end
end
