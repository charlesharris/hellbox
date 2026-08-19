# One net's proposal about what a disc is, with its reasoning.
#
# Kept rather than collapsed into a winner. A wrong name in the library has to
# be traceable to the evidence that produced it, and agreement between two nets
# is worth more than either being confident -- which cannot be recomputed once
# the losers are thrown away.
class Candidate < ApplicationRecord
  belongs_to :disc

  # "discname" covers both the DVD text data manager and a Blu-ray's
  # bdmt_eng.xml: the design split those into ifo/bdmt, but they are one kind
  # of evidence and one implementation.
  NETS = %w[discname label shape menu credits provider set].freeze

  validates :net, inclusion: { in: NETS }

  def confidence_pct = (confidence * 100).round
end
