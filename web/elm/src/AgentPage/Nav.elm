module AgentPage.Nav exposing (Item, items)

{-| The one hand-maintained list of agent-platform destinations.

Both navs that reach the platform read this list: the sidebar's always-visible
agent section (`SideBar.SideBar`) and the in-page strip every agent page shows
under its title (`AgentPage.Chrome`). They used to be two literal lists with two
sets of labels, so adding a page meant editing both — and the labels drifted
("workflows" in one, "Agent workflows" in the other) for the same route.

`id` is the stable half of each nav element's DOM id; each nav prefixes it with
its own scope so the two navs never collide on a page that renders both.

-}

import Routes


type alias Item =
    { id : String
    , route : Routes.Route
    , label : String
    }


items : List Item
items =
    [ { id = "agent-platform"
      , route = Routes.Agent
      , label = "Agent workflows"
      }
    , { id = "agent-tickets"
      , route = Routes.AgentTickets
      , label = "Ticket queue"
      }
    , { id = "agent-reviews"
      , route = Routes.AgentReviews { teamName = "main" }
      , label = "Review queue"
      }
    , { id = "agent-experiments"
      , route = Routes.AgentExperiments
      , label = "Experiment laboratory"
      }
    ]
