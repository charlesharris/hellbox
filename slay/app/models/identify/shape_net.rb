module Identify
  # Decides what a disc holds from the shape of its titles.
  #
  # Structure is all there is to go on: a disc carries no statement of what it
  # is, and its label is frequently no help. A film disc is lopsided — one title
  # dwarfing the rest. An episode disc is level.
  #
  # Proposes a Kind and never a title, because runtimes cannot name anything.
  class ShapeNet
    FEATURE_RATIO   = 2.0
    MIN_FEATURE_S   = 45 * 60
    EPISODE_SPREAD  = 0.25
    MIN_EPISODE_S   = 10 * 60
    MAX_EPISODE_S   = 90 * 60

    def name = "shape"

    def identify(disc)
      durations = disc.disc_titles.map(&:duration_seconds).reject(&:zero?).sort.reverse
      return [] if durations.empty?

      kind = classify(durations)
      return [] if kind.nil?

      [CandidateProposal.new(
        net: name, kind: kind, confidence: 0.45,
        why: kind == "series" ? "several titles of much the same length" :
                                "one title dwarfs the rest of the disc"
      )]
    end

    # Ported from the Go original, including both corrections it needed.
    def classify(d)
      longest = d.first
      return longest >= MIN_FEATURE_S ? "movie" : nil if d.size == 1

      median = d[d.size / 2]

      # A television disc can be lopsided too, when it carries a double-length
      # episode. Firefly disc 1 is a 1:26 pilot against three 43-minute
      # episodes: a longest title 1.975x the median, which the ratio test below
      # nearly calls a film. What tells them apart is the remainder — strip the
      # longest and an episode disc is still episodes.
      return "series" if d.size > 1 && episodes?(d[1..])

      return "movie" if longest >= MIN_FEATURE_S && longest >= FEATURE_RATIO * median
      return "series" if episodes?(d)
      nil
    end

    private

    def episodes?(d)
      return false if d.size < 2
      median = d[d.size / 2]
      return false if median < MIN_EPISODE_S || median > MAX_EPISODE_S
      alike = d.count { |x| x >= MIN_EPISODE_S && ((x - median).abs.to_f / median) <= EPISODE_SPREAD }
      alike >= 2 && alike * 2 >= d.size
    end
  end
end
