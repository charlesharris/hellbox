module Identify
  # Picks a winner from what the nets proposed.
  #
  # Agreement counts for more than confidence. Two nets reaching the same title
  # from different evidence — a label reading "Roman Holiday" and a content hash
  # matching Roman Holiday — is a far stronger signal than one net being sure,
  # so agreeing proposals are merged and their confidence raised rather than
  # made to compete.
  class Resolver
    AGREEMENT_BONUS = 0.15
    CEILING         = 0.95
    CONTESTED_GAP   = 0.2

    Result = Struct.new(:best, :all, :contested, keyword_init: true)

    def self.call(proposals) = new(proposals).call

    def initialize(proposals)
      @proposals = Array(proposals).reject { |p| p.title.to_s.strip.empty? && p.kind.nil? }
    end

    def call
      return Result.new(best: nil, all: [], contested: false) if @proposals.empty?

      titled, kind_only = @proposals.partition { |p| p.title.to_s.strip.present? }

      merged = titled
               .group_by(&:normalised_title)
               .values
               .map { |group| merge(group) }
               .sort_by { |p| -p.confidence }

      # A net that proposes only a Kind — ShapeNet — informs the winner rather
      # than competing with it. Runtimes cannot name anything, so a shape
      # proposal has no title to win with, and dropping it would throw away the
      # one net that can tell a box set from a film.
      if merged.any? && (shape = kind_only.find { |p| p.kind })
        merged.first.kind ||= shape.kind
        merged.first.why += " (#{shape.why})"
      end

      merged = kind_only if merged.empty?

      Result.new(
        best: merged.first,
        all: merged,
        # Contested when the runner-up is close enough that picking the leader
        # is nearly a coin toss. Deliberately generous: a wrong name filed
        # silently is worse than one flagged for a look.
        contested: merged.size > 1 &&
                   (merged[0].confidence - merged[1].confidence) < CONTESTED_GAP
      )
    end

    private

    def merge(group)
      group = group.sort_by { |p| -p.confidence }
      best = group.first.dup
      return best if group.size == 1

      # A proposal that failed its own evidence check is testimony, not
      # corroboration.
      #
      # It stays in the group and it stays visible — an extended cut is not a
      # wrong film, and only a person can tell those apart. But it must not
      # make anyone more confident and it must not fill in fields nobody else
      # supplied, and until this it did both. A provider match rejected on
      # runtime merged with whatever named the same film, counted as a second
      # net agreeing, and handed over the year and the tmdb id that end up in
      # the NFO. On this project's own worked example that was enough to file a
      # 1984 disc as the 2010 remake, auto-confirmed, at 0.95.
      corroborating = group.reject(&:rejected?)
      others        = group.drop(1).reject(&:rejected?)

      nets = corroborating.map(&:net).uniq
      if nets.size > 1
        # No amount of agreement makes a guess a fact, and leaving room below 1
        # keeps a later net that reads the actual title able to outrank a
        # consensus of inferences.
        best.confidence = [best.confidence + AGREEMENT_BONUS * (nets.size - 1), CEILING].min
        best.why += " (agreed by #{nets.join(', ')})"
      end

      # Details the leader lacked are taken from the others: a content hash may
      # know the year while the label knows the disc number.
      others.each do |p|
        best.year        ||= p.year
        best.season      ||= p.season
        best.disc_number ||= p.disc_number
        best.kind        ||= p.kind
        # The provider id in particular: a label net that reads the title off
        # the disc routinely outranks the provider net that confirmed it, and
        # dropping the id there would lose the one field the library needs.
        best.provider    ||= p.provider
        best.provider_id ||= p.provider_id
      end

      # Merging keeps only the winner, so anything rejected would otherwise
      # leave the record entirely and take its reasoning with it. Naming it
      # here is what lets someone reading a wrong entry in the library see that
      # a plausible alternative was considered and why it lost.
      rejected = group.select(&:rejected?)
      if rejected.any?
        best.why += " (set aside: #{rejected.map(&:why).join('; ')})"
      end

      best
    end
  end
end
