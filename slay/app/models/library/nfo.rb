module Library
  # NFO sidecars: what hellbox tells Jellyfin rather than lets it guess.
  #
  # This is the point of choosing to be authoritative. Jellyfin matching a
  # filename against its own providers is a good default and a bad outcome for
  # a shelf like this one -- two films share a name, a series has a regional
  # retitling, a disc holds the 1984 film and the 2010 one is more popular. An
  # NFO carrying the provider id ends every one of those arguments before it
  # starts.
  #
  # It writes only what the catalog actually knows. Plot and air date belong
  # here too and are not written, because nothing yet persists the provider's
  # episode records -- `episodes` is a table with no writer. Writing an empty
  # <plot/> would teach Jellyfin that there is no plot, which is worse than
  # staying quiet and letting it fill in what it can.
  class Nfo
    def self.movie(title:, year: nil, provider: nil, provider_id: nil)
      document("movie", [
        tag("title", title),
        tag("year", year.to_i.positive? ? year.to_i : nil),
        *ids(provider, provider_id)
      ])
    end

    def self.tvshow(title:, year: nil, provider: nil, provider_id: nil)
      document("tvshow", [
        tag("title", title),
        tag("year", year.to_i.positive? ? year.to_i : nil),
        *ids(provider, provider_id)
      ])
    end

    def self.episode(show:, season:, episode:, title: nil)
      document("episodedetails", [
        # Jellyfin shows <title> as the episode name. With none typed, the
        # SxxExx designation is a better label than the filename it would
        # otherwise fall back to.
        tag("title", title.presence || format("Episode %d", episode.to_i)),
        tag("showtitle", show),
        tag("season", season.to_i),
        tag("episode", episode.to_i)
      ])
    end

    # An extra is titled and nothing more. Giving it a provider id would tie a
    # featurette to the film's own record, which is how it ends up offered as
    # another version of the feature.
    def self.extra(title:)
      document("movie", [tag("title", title)])
    end

    # `uniqueid` is what current Jellyfin reads; the bare `<tmdbid>` is the
    # older form. Both are written because the cost is one line and the failure
    # mode of picking wrong is silent.
    def self.ids(provider, provider_id)
      return [] if provider.blank? || provider_id.blank?
      [
        %(<uniqueid type="#{escape(provider)}" default="true">#{escape(provider_id)}</uniqueid>),
        tag("#{provider}id", provider_id)
      ]
    end

    def self.tag(name, value)
      return nil if value.nil? || value.to_s.strip.empty?
      "<#{name}>#{escape(value)}</#{name}>"
    end

    def self.document(root, children)
      body = children.compact.map { |c| "  #{c}" }.join("\n")
      <<~XML
        <?xml version="1.0" encoding="UTF-8" standalone="yes"?>
        <#{root}>
        #{body}
        </#{root}>
      XML
    end

    # CGI rather than ERB::Util, whose xml_escape is an ActiveSupport addition
    # and not there in plain Ruby. This escapes the five characters that matter
    # and nothing else, which is what an XML text node needs.
    def self.escape(value) = CGI.escapeHTML(value.to_s)

    private_class_method :tag, :document, :escape, :ids
  end
end
