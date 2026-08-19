module Identify
  # One net's proposal about what a disc is, before anything is persisted.
  #
  # Kept as a plain object rather than an ActiveRecord Candidate so nets can be
  # run and re-run without touching the database — which is what makes it
  # possible to rewrite identification and replay it over the whole collection.
  CandidateProposal = Struct.new(
    :net, :title, :year, :kind, :season, :disc_number, :confidence, :why,
    # Only a net that consulted a provider can fill these in, and only those
    # two fields survive into the NFO — which is the point of the whole
    # exercise, since a tmdb id is what stops Jellyfin re-matching the file by
    # its name.
    :provider, :provider_id,
    # Set by a net that checked its own proposal against evidence and found it
    # wanting: a provider match whose runtime does not agree with the disc,
    # most often.
    #
    # Such a proposal is still worth reporting — an extended cut is not a wrong
    # film, and a person reading the review queue can tell the difference where
    # this cannot. What it must not do is corroborate anything. The resolver
    # treats it as testimony rather than agreement: it earns no confidence for
    # the proposal it sits beside, and it donates none of its fields.
    :rejected,
    keyword_init: true
  ) do
    def initialize(**kw)
      super
      self.confidence ||= 0.0
      self.why ||= ""
      self.rejected = false if rejected.nil?
    end

    def rejected? = !!rejected

    # What two nets must share to count as agreeing. "Roman Holiday" and
    # "ROMAN_HOLIDAY" are the same claim from different evidence.
    def normalised_title
      title.to_s.downcase.gsub(/[^a-z0-9]+/, " ").strip
    end
  end
end
