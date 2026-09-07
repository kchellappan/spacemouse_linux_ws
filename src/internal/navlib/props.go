// Package navlib models the 3Dconnexion Navigation Library property interface
// as exposed to web clients by 3DconnexionJS.
//
// The key inversion to keep in mind: the *page* serves these properties and
// this process consumes them. We read the camera and scene, compute a new
// camera pose, and write it back. See docs/01-architecture-and-data-model.md.
package navlib

// Properties the client can be asked to read. Names are taken from
// 3dconnexion.js clientFnRead and match the constants in navlib.h.
const (
	PropViewAffine            = "view.affine"
	PropViewConstructionPlane = "view.constructionPlane"
	PropViewExtents           = "view.extents"
	PropViewFOV               = "view.fov"
	PropViewFrustum           = "view.frustum"
	PropViewPerspective       = "view.perspective"
	PropViewTarget            = "view.target"
	PropViewRotatable         = "view.rotatable"

	PropModelExtents       = "model.extents"
	PropModelFloorPlane    = "model.floorPlane"
	PropModelUnitsToMeters = "model.unitsToMeters"

	PropPivotPosition = "pivot.position"
	PropPivotVisible  = "pivot.visible"

	PropHitLookAt        = "hit.lookat"
	PropHitLookFrom      = "hit.lookfrom"
	PropHitDirection     = "hit.direction"
	PropHitAperture      = "hit.aperture"
	PropHitSelectionOnly = "hit.selectionOnly"

	PropSelectionAffine  = "selection.affine"
	PropSelectionEmpty   = "selection.empty"
	PropSelectionExtents = "selection.extents"

	PropPointerPosition  = "pointer.position"
	PropCoordinateSystem = "coordinateSystem"
	PropViewsFront       = "views.front"

	PropFrameTimingSource = "frame.timingSource"
	PropFrameTime         = "frame.time"

	PropMotion      = "motion"
	PropTransaction = "transaction"

	PropCommandsActiveCommand = "commands.activeCommand"
	PropEventsKeyPress        = "events.keyPress"
	PropEventsKeyRelease      = "events.keyRelease"
	PropSettingsChanged       = "settings.changed"
)

// The two RPC procedures the driver invokes on the page.
const (
	procRead   = "self:read"
	procUpdate = "self:update"
)

// V3DK button codes, from 3dconnexion.js.
const (
	V3DKMenu = 0x1e
	V3DKFit  = 0x1f
)
