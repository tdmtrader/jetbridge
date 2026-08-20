module PipelineRuns.PipelineRuns exposing (Model, change, documentTitle, getUpdateMessage, handleCallback, init, subscriptions, tooltip, update, view)
import Application.Models exposing (Session)
import Concourse
import Concourse.BuildStatus as BuildStatus
import Concourse.Pagination as Pagination exposing (Page, Paginated)
import Concourse.PipelineRun exposing (PipelineRun)
import Dict
import Duration
import Html exposing (Html)
import Html.Attributes exposing (attribute, class, disabled, href, id, style, tabindex, type_, value)
import Html.Events exposing (onClick, onInput, onSubmit)
import Http
import Json.Encode
import Message.Callback exposing (Callback(..))
import Message.Effects exposing (Effect(..))
import Message.Message exposing (Message(..))
import Message.Subscription exposing (Subscription)
import PipelineRuns.RunForm as RunForm
import PipelineRuns.Styles as Styles
import RemoteData exposing (WebData)
import Routes
import SideBar.SideBar as SideBar
import StrictEvents
import Time
import Tooltip
import UpdateMsg exposing (UpdateMsg(..))
import Views.Styles as ViewStyles
type alias Model =
    { pipelineId : Concourse.PipelineIdentifier
    , page : Page
    , template : WebData Concourse.Pipeline
    , runs : WebData (Paginated PipelineRun)
    , form : RunForm.Model
    , formInitialized : Bool
    , formOpen : Bool
    , pending : Bool
    , refreshing : Bool
    , error : Maybe String
    , validation : Maybe RunForm.ValidationError
    , now : Maybe Time.Posix
    }
pageLimit : Int
pageLimit =
    50
init : { id : Concourse.PipelineIdentifier, page : Maybe Page } -> ( Model, List Effect )
init flags =
    let
        page =
            Maybe.withDefault { direction = Pagination.ToMostRecent, limit = pageLimit } flags.page
    in
    ( { pipelineId = flags.id
      , page = page
      , template = RemoteData.Loading
      , runs = RemoteData.Loading
      , form = RunForm.init []
      , formInitialized = False
      , formOpen = False
      , pending = False
      , refreshing = False
      , error = Nothing
      , validation = Nothing
      , now = Nothing
      }
    , [ FetchPipeline flags.id, FetchPipelineRuns flags.id page, GetCurrentTime ]
    )
change : { id : Concourse.PipelineIdentifier, page : Maybe Page } -> ( Model, List Effect ) -> ( Model, List Effect )
change flags ( model, effects ) =
    let
        page =
            Maybe.withDefault { direction = Pagination.ToMostRecent, limit = pageLimit } flags.page
    in
    if flags.id == model.pipelineId && page == model.page then
        ( model, effects )
    else if flags.id == model.pipelineId then
        ( { model | page = page, runs = RemoteData.Loading, error = Nothing }
        , effects ++ [ FetchPipelineRuns flags.id page ]
        )
    else
        init flags
            |> (\( newModel, newEffects ) -> ( newModel, effects ++ newEffects ))
handleCallback : Callback -> ( Model, List Effect ) -> ( Model, List Effect )
handleCallback callback ( model, effects ) =
    case callback of
        PipelineFetched (Ok pipeline) ->
            ( { model
                | template = RemoteData.Success pipeline
                , form = if model.formInitialized then model.form else RunForm.init pipeline.paramsSchema
                , formInitialized = True
                , refreshing = False
              }
            , effects
            )
        PipelineFetched (Err err) ->
            ( { model | template = RemoteData.Failure err, refreshing = False }, effects )
        PipelineRunsFetched (Ok ( _, runs )) ->
            ( { model | runs = RemoteData.Success runs, error = if model.refreshing then model.error else Nothing }, effects )
        PipelineRunsFetched (Err err) ->
            ( { model | runs = RemoteData.Failure err, error = if model.refreshing then model.error else Just "Unable to load run history." }, effects )
        PipelineRunCreated (Ok run) ->
            ( { model | pending = False, refreshing = False }
            , effects ++ [ NavigateTo <| Routes.toString <| Routes.PipelineRun { template = model.pipelineId, number = run.number } ]
            )
        PipelineRunCreated (Err err) ->
            let
                message =
                    httpMessage err "Unable to start a run."
            in
            ( { model | pending = False, refreshing = refreshableError err, error = Just message, validation = Nothing }
            , effects ++ (if refreshableError err then [ FetchPipeline model.pipelineId ] else []) ++ [ Focus "run-form-error" ]
            )
        GotCurrentTime now ->
            ( { model | now = Just now }, effects )
        _ ->
            ( model, effects )
update : Message -> ( Model, List Effect ) -> ( Model, List Effect )
update message ( model, effects ) =
    case message of
        OpenPipelineRunForm ->
            ( { model | formOpen = True, error = Nothing, validation = Nothing }, effects )
        SetPipelineRunParam name input ->
            ( { model | form = RunForm.set name input model.form, validation = Nothing }, effects )
        SubmitPipelineRun ->
            submit model effects
        RetryPipelineRuns ->
            ( { model | template = RemoteData.Loading, runs = RemoteData.Loading, error = Nothing, refreshing = False }
            , effects ++ [ FetchPipeline model.pipelineId, FetchPipelineRuns model.pipelineId model.page ]
            )
        _ ->
            ( model, effects )
submit : Model -> List Effect -> ( Model, List Effect )
submit model effects =
    if model.pending || model.refreshing then
        ( model, effects )
    else case model.template of
        RemoteData.Success template ->
            case RunForm.encode template.paramsSchema model.form of
                Err validation ->
                    ( { model | error = Just validation.message, validation = Just validation }
                    , effects ++ [ Focus <| Maybe.withDefault "run-form-error" validation.fieldId ]
                    )
                Ok vars ->
                    if creationHold template then
                        ( { model | error = Just (holdReason template), validation = Nothing }, effects ++ [ Focus "run-form-error" ] )
                    else
                        ( { model | pending = True, error = Nothing, validation = Nothing }
                        , effects ++ [ CreatePipelineRun model.pipelineId vars ]
                        )
        _ ->
            ( model, effects )
getUpdateMessage : Model -> UpdateMsg
getUpdateMessage model =
    case model.template of
        RemoteData.Failure (Http.BadStatus { status }) ->
            if status.code == 404 then
                NotFound
            else
                AOK
        _ ->
            AOK
documentTitle : Model -> String
documentTitle model =
    model.pipelineId.pipelineName ++ " runs"
subscriptions : List Subscription
subscriptions =
    []
tooltip : Model -> Session -> Maybe Tooltip.Tooltip
tooltip _ _ =
    Nothing
view : Session -> Model -> Html Message
view session model =
    let route = Routes.PipelineRuns { id = model.pipelineId, page = Just model.page } in
    Html.div [ style "height" "100%" ]
        [ Html.div (id "page-including-top-bar" :: ViewStyles.pageIncludingTopBar)
            [ Html.div (id "top-bar-app" :: ViewStyles.topBar False) [ SideBar.sideBarIcon session, Html.div [ id "breadcrumbs" ] [ Html.text (model.pipelineId.pipelineName ++ " runs") ] ] ]
        , Html.div (id "page-below-top-bar" :: ViewStyles.pageBelowTopBar route)
            [ SideBar.view session (Just model.pipelineId), viewBody model ]
        ]
viewBody : Model -> Html Message
viewBody model =
    Html.main_ (id "pipeline-runs" :: Styles.body)
        [ Html.h1 [] [ Html.text (model.pipelineId.pipelineName ++ " runs") ]
        , Html.div [ id "run-form-error", attribute "aria-live" "polite", tabindex -1, style "outline" "none" ] [ Html.text (Maybe.withDefault "" model.error) ]
        , viewContents model
        ]
viewContents : Model -> Html Message
viewContents model =
    case model.template of
        RemoteData.Loading ->
            Html.p [] [ Html.text "Loading run history…" ]
        RemoteData.NotAsked ->
            Html.p [] [ Html.text "Loading run history…" ]
        RemoteData.Failure err ->
            viewTemplateError err
        RemoteData.Success template ->
            Html.div [] [ viewStart template model, viewHistory model ]
viewHistory : Model -> Html Message
viewHistory model =
    case model.runs of
        RemoteData.Success runs ->
            Html.div []
                [ if List.isEmpty runs.content then Html.p [] [ Html.text "No runs yet." ] else viewTable model.pipelineId model.now runs.content
                , viewPager model.pipelineId runs.pagination
                ]
        RemoteData.Failure _ ->
            Html.button (Styles.button ++ [ onClick RetryPipelineRuns, attribute "aria-label" "Retry run history" ]) [ Html.text "Retry" ]
        _ ->
            Html.p [] [ Html.text "Loading run history…" ]
viewTemplateError : Http.Error -> Html Message
viewTemplateError err =
    case err of
        Http.BadStatus { status } ->
            if status.code == 403 then
                Html.p [] [ Html.text "You do not have access to this pipeline." ]
            else
                retryButton
        _ ->
            retryButton
retryButton : Html Message
retryButton =
    Html.div []
        [ Html.p [] [ Html.text "Unable to load this template." ]
        , Html.button (Styles.button ++ [ onClick RetryPipelineRuns, attribute "aria-label" "Retry template" ]) [ Html.text "Retry" ]
        ]
viewStart : Concourse.Pipeline -> Model -> Html Message
viewStart template model =
    if template.template /= Just True || not template.canCreateRun then
        Html.text ""
    else if model.formOpen then
        viewForm template model
    else
        Html.button (Styles.button ++ [ onClick OpenPipelineRunForm ]) [ Html.text "Start a run" ]
viewForm : Concourse.Pipeline -> Model -> Html Message
viewForm template model =
    Html.form
        (Styles.form
            ++ [ onSubmit SubmitPipelineRun
               , attribute "aria-busy" (if model.pending then "true" else "false")
               ]
        )
        ([ Html.h2 [] [ Html.text "Start a run" ]
         ]
            ++ List.map (viewField model) template.paramsSchema
            ++ [ Html.button
                    (Styles.button
                        ++ [ type_ "submit"
                           , disabled (model.pending || model.refreshing || creationHold template)
                           ]
                    )
                    [ Html.text "Start run" ]
               , if creationHold template then
                    Html.span Styles.hold [ Html.text (holdReason template) ]
                 else
                    Html.text ""
               ]
        )
viewField : Model -> Concourse.ParamSchema -> Html Message
viewField model schema =
    let
        fieldId =
            "run-param-" ++ schema.name
        descriptionId =
            fieldId ++ "-description"
        errorId =
            fieldId ++ "-error"
        describedBy =
            if Maybe.map .fieldId model.validation == Just (Just fieldId) then descriptionId ++ " " ++ errorId else descriptionId
        field =
            if schema.type_ == Concourse.EnumParam then
                Html.select
                    [ id fieldId
                    , value (RunForm.value schema.name model.form)
                    , onInput (SetPipelineRunParam schema.name)
                    , attribute "aria-describedby" describedBy
                    , disabled model.pending
                    ]
                    ((if schema.default == Nothing then [ Html.option [ value "" ] [ Html.text ("Select " ++ schema.name) ] ] else []) ++ List.map (\option -> Html.option [ value (jsonText option) ] [ Html.text (jsonText option) ]) schema.values)
            else
                Html.input
                    [ id fieldId
                    , type_ (inputType schema.type_)
                    , value (RunForm.value schema.name model.form)
                    , onInput (SetPipelineRunParam schema.name)
                    , attribute "aria-describedby" describedBy
                    , disabled model.pending
                    ]
                    []
    in
    Html.div [ style "margin" "10px 0" ]
        [ Html.label [ Html.Attributes.for fieldId ] [ Html.text schema.name ]
        , field
        , Html.div [ id descriptionId ] [ Html.text (Maybe.withDefault "" schema.description) ]
        , Html.div [ id errorId, attribute "role" "alert" ] [ Html.text <| validationText fieldId model.validation ]
        ]
inputType : Concourse.ParamType -> String
inputType paramType =
    case paramType of
        Concourse.NumberParam -> "number"
        Concourse.BoolParam -> "text"
        _ -> "text"
jsonText : Concourse.JsonValue -> String
jsonText json =
    case json of
        Concourse.JsonString string -> string
        Concourse.JsonNumber number -> String.fromFloat number
        Concourse.JsonObject _ -> Json.Encode.encode 0 (Concourse.encodeJsonValue json)
        Concourse.JsonRaw raw -> Json.Encode.encode 0 raw
validationText : String -> Maybe RunForm.ValidationError -> String
validationText fieldId validation =
    case validation of
        Just error -> if error.fieldId == Just fieldId then error.message else ""
        Nothing -> ""
creationHold : Concourse.Pipeline -> Bool
creationHold template =
    template.paused || template.archived
holdReason : Concourse.Pipeline -> String
holdReason template =
    if template.archived then "This template is archived."
    else "This template is paused."
viewTable : Concourse.PipelineIdentifier -> Maybe Time.Posix -> List PipelineRun -> Html Message
viewTable template now runs =
    Html.table Styles.table
        [ Html.thead []
            [ Html.tr []
                [ header "Run" "col"
                , header "Status" "col"
                , header "Parameters" "col"
                , header "Duration" "col"
                , header "Created by" "col"
                ]
            ]
        , Html.tbody [] (List.map (viewRun template now) runs)
        ]
header : String -> String -> Html Message
header textValue scopeValue =
    Html.th [ attribute "scope" scopeValue ] [ Html.text textValue ]
viewRun : Concourse.PipelineIdentifier -> Maybe Time.Posix -> PipelineRun -> Html Message
viewRun template now run =
    Html.tr []
        [ Html.td [] [ viewRunNumber template run ]
        , Html.td [ class (BuildStatus.show run.status) ] [ Html.text (BuildStatus.show run.status) ]
        , Html.td [] [ Html.text (Concourse.hyphenNotation run.params) ]
        , Html.td [] [ Html.text (runDuration now run) ]
        , Html.td [] [ Html.text (Maybe.withDefault "—" run.createdBy) ]
        ]
viewRunNumber : Concourse.PipelineIdentifier -> PipelineRun -> Html Message
viewRunNumber template run =
    case run.instanceRef of
        Just _ ->
            Html.a
                [ href (Routes.toString <| Routes.PipelineRun { template = template, number = run.number })
                , attribute "aria-label" ("run #" ++ String.fromInt run.number)
                ]
                [ Html.text ("#" ++ String.fromInt run.number) ]
        Nothing ->
            Html.span [] [ Html.text ("#" ++ String.fromInt run.number) ]
runDuration : Maybe Time.Posix -> PipelineRun -> String
runDuration now run =
    case run.completedAt of
        Just completedAt ->
            Duration.format (Duration.between run.createdAt completedAt)
        Nothing ->
            now |> Maybe.map (\current -> Duration.format (Duration.between run.createdAt current) ++ " ago") |> Maybe.withDefault "just now"
viewPager : Concourse.PipelineIdentifier -> Pagination.Pagination -> Html Message
viewPager pipelineId pagination =
    Html.nav [ attribute "aria-label" "Run history pages" ]
        [ pagerLink "Previous page" pipelineId pagination.previousPage
        , pagerLink "Next page" pipelineId pagination.nextPage
        ]
pagerLink : String -> Concourse.PipelineIdentifier -> Maybe Page -> Html Message
pagerLink label pipelineId maybePage =
    case maybePage of
        Just page ->
            let route = Routes.PipelineRuns { id = pipelineId, page = Just page } in
            Html.a [ href (Routes.toString route), StrictEvents.onLeftClick (GoToRoute route), attribute "aria-label" label, style "outline" "auto" ] [ Html.text label ]
        Nothing ->
            Html.span [] []
httpMessage : Http.Error -> String -> String
httpMessage err fallback =
    case err of
        Http.BadStatus response ->
            let body = String.trim response.body in
            if body == "" then
                if response.status.message == "" then fallback else response.status.message
            else
                body
        _ -> fallback
refreshableError : Http.Error -> Bool
refreshableError err =
    case err of
        Http.BadStatus { status } -> status.code == 400 || status.code == 409
        _ -> False
